# The workloads Foundry host (aws-design §6.2, refactor Phase 7): one t4g.small
# with a separate data volume, an Elastic IP and a least-privilege role.
# Rebuilding the host never touches the data volume.

locals {
  # AL2023 namespaces Node.js: nodejs24 installs /usr/bin/node-24, and `node`
  # is only an alternatives link. The unit calls the namespaced binary so a
  # later alternatives change can't switch Foundry's runtime.
  node_bin = "/usr/bin/node-24"

  # D15: a plain systemd unit, so the host-contract preflight line is dropped.
  # Nothing else supervises Foundry while the host runs, so a crash restarts
  # it after 5 seconds, as on the old host. A clean `systemctl stop` never
  # restarts it.
  foundry_unit_source = file("${path.module}/../host/templates/foundry.service")
  foundry_unit = replace(
    replace(
      replace(local.foundry_unit_source, "ExecStartPre=+/usr/local/sbin/foundry-host preflight\n", ""),
      "ExecStart=/usr/bin/node ",
      "ExecStart=${local.node_bin} ",
    ),
    "Restart=no\n",
    "Restart=on-failure\nRestartSec=5\n",
  )

  # The data volume's stable NVMe link. fstab mounts this exact volume, never a
  # label or UUID: a restore-test volume made from its snapshot carries the same
  # label and UUID and must never be taken for /foundry.
  data_volume_device = "/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${replace(aws_ebs_volume.data.id, "-", "")}"

  user_data = templatefile("${path.module}/templates/user-data.sh", {
    node_bin           = local.node_bin
    foundry_unit       = local.foundry_unit
    proxy_setup        = file("${path.module}/templates/foundry-proxy-setup.sh")
    cert_deploy        = file("${path.module}/templates/foundry-cert-deploy.sh")
    data_volume_device = local.data_volume_device
  })

  # Workloads us-west-2 alerts topic, owned by aws-setup (aws-design §7).
  alerts_topic_arn = "arn:aws:sns:${local.region}:${local.workloads_account_id}:alerts"
}

# --- Instance role (least privilege, aws-design §5.3 and G7) ---

data "aws_iam_policy_document" "host_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "host" {
  name                  = "foundry-host"
  description           = "Foundry VTT host instance role"
  assume_role_policy    = data.aws_iam_policy_document.host_trust.json
  force_detach_policies = true
}

locals {
  # Never AmazonSSMManagedInstanceCore: it grants ssm:GetParameter(s) on *, and
  # the account's aws/ssm key would let the host read every app's SecureStrings.
  host_managed_policy_arns = ["arn:aws:iam::aws:policy/AmazonSSMManagedEC2InstanceDefaultPolicy"]
}

resource "aws_iam_role_policy_attachment" "host" {
  for_each = toset(local.host_managed_policy_arns)

  role       = aws_iam_role.host.name
  policy_arn = each.value
}

resource "aws_iam_role_policy_attachments_exclusive" "host" {
  role_name   = aws_iam_role.host.name
  policy_arns = local.host_managed_policy_arns

  depends_on = [aws_iam_role_policy_attachment.host]
}

# Reads only Foundry's own parameters. There is no KMS statement: SSM decrypts
# SecureStrings under the account's aws/ssm key for any account principal, so
# the path scope is the control (aws-design §6.2). Never widen it to *.
data "aws_iam_policy_document" "host" {
  statement {
    sid = "ReadFoundryParameters"
    actions = [
      "ssm:GetParameter",
      "ssm:GetParameters",
      "ssm:GetParametersByPath",
    ]
    resources = [
      "arn:aws:ssm:${local.region}:${local.workloads_account_id}:parameter/foundry",
      "arn:aws:ssm:${local.region}:${local.workloads_account_id}:parameter/foundry/*",
    ]
  }
}

resource "aws_iam_role_policy" "host" {
  name   = "foundry-host"
  role   = aws_iam_role.host.id
  policy = data.aws_iam_policy_document.host.json
}

# Deletes any inline policy not listed here, so one added by hand shows up as
# drift (G7). foundry-host is the only inline policy.
resource "aws_iam_role_policies_exclusive" "host" {
  role_name    = aws_iam_role.host.name
  policy_names = [aws_iam_role_policy.host.name]
}

resource "aws_iam_instance_profile" "host" {
  name = "foundry-host"
  role = aws_iam_role.host.name
}

# --- Instance ---

resource "aws_instance" "host" {
  ami                  = var.ami_id
  instance_type        = "t4g.small"
  subnet_id            = data.aws_subnet.host.id
  iam_instance_profile = aws_iam_instance_profile.host.name
  # No key_name: admin access is Session Manager only.
  vpc_security_group_ids = [aws_security_group.foundry.id]

  # EC2's default, stated because a stop from The Lobby powers the host off
  # from inside (the foundry-stop document, ADR 0006): with terminate here,
  # that would destroy the host instead.
  instance_initiated_shutdown_behavior = "stop"

  # First-boot setup only; it holds no secrets and never formats a disk.
  user_data = local.user_data

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "disabled"
  }

  root_block_device {
    volume_type           = "gp3"
    volume_size           = 10
    encrypted             = true
    delete_on_termination = true

    # project=foundry comes from default_tags; DLM snapshots both volumes by it.
    tags = {
      Name = "foundry-root"
    }
  }

  tags = {
    Name = "foundry"
  }

  lifecycle {
    # A rebuild is always a deliberate `tofu plan -replace=aws_instance.host`.
    ignore_changes = [ami, user_data]

    precondition {
      condition = (
        !strcontains(local.foundry_unit, "ExecStartPre=") &&
        strcontains(local.foundry_unit, "ExecStart=${local.node_bin} ") &&
        strcontains(local.foundry_unit, "\nRestart=on-failure\nRestartSec=5\n") &&
        !strcontains(local.foundry_unit, "\nRestart=no\n")
      )
      error_message = "host/templates/foundry.service changed: the preflight line must be removed, ExecStart must call ${local.node_bin}, and Restart must be on-failure with RestartSec=5. Update the replace() calls in host.tf."
    }

    # user data writes each script with a quoted heredoc that ends at that
    # line, so the script must never contain it, and must end with a newline
    # so the terminator starts a line of its own.
    precondition {
      condition = alltrue([
        for script, terminator in {
          "templates/foundry-proxy-setup.sh" = "PROXY"
          "templates/foundry-cert-deploy.sh" = "HOOK"
        } :
        endswith(file("${path.module}/${script}"), "\n") && !can(regex("(?m)^${terminator}$", file("${path.module}/${script}")))
      ])
      error_message = "templates/foundry-proxy-setup.sh and templates/foundry-cert-deploy.sh must end with a newline and never contain the line PROXY or HOOK, which end their heredocs in templates/user-data.sh."
    }
  }

  # Outbound access and the SSM policy exist before first-boot user data runs.
  depends_on = [
    aws_vpc_security_group_egress_rule.all_ipv4,
    aws_iam_role_policy_attachment.host,
  ]
}

# --- Data volume (worlds, Config, the app tree) ---

resource "aws_ebs_volume" "data" {
  availability_zone = data.aws_subnet.host.availability_zone
  type              = "gp3"
  size              = 20
  encrypted         = true

  tags = {
    Name = "foundry-data"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_volume_attachment" "data" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.host.id

  # Detach only from a stopped instance, so the filesystem is never torn.
  stop_instance_before_detaching = true
}

# --- Public address (Cloudflare A record, DNS only, set by hand: D19) ---

resource "aws_eip" "host" {
  domain = "vpc"

  tags = {
    Name = "foundry"
  }

  lifecycle {
    # The hand-set Cloudflare A record points at this exact address (D19).
    prevent_destroy = true
  }
}

resource "aws_eip_association" "host" {
  allocation_id = aws_eip.host.allocation_id
  instance_id   = aws_instance.host.id
}

# --- Status check alarm (D18) ---

resource "aws_cloudwatch_metric_alarm" "host_status_check" {
  alarm_name          = "foundry-status-check-failed"
  alarm_description   = "Foundry host failed an EC2 status check"
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed"
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 2
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 1
  treat_missing_data  = "missing"

  dimensions = {
    InstanceId = aws_instance.host.id
  }

  alarm_actions = [local.alerts_topic_arn]
  ok_actions    = [local.alerts_topic_arn]
}
