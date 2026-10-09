# Offline plan checks. The provider is mocked, so no AWS credentials or calls
# are needed: run `tofu -chdir=infra test` after `init -backend=false`.

mock_provider "aws" {
  mock_resource "aws_s3_bucket" {
    defaults = {
      arn = "arn:aws:s3:::foundry-tofu-state-793680745829"
    }
  }

  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::793680745829:role/mock"
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }

  mock_data "aws_vpc" {
    defaults = {
      id = "vpc-033b6d36d513f2b49"
    }
  }

  mock_data "aws_subnet" {
    defaults = {
      id                = "subnet-09419fb2922a65f2a"
      availability_zone = "us-west-2a"
    }
  }

  mock_resource "aws_ebs_volume" {
    defaults = {
      id = "vol-0123456789abcdef0"
    }
  }

  mock_resource "aws_ssm_document" {
    defaults = {
      arn = "arn:aws:ssm:us-west-2:793680745829:document/foundry-shutdown"
    }
  }

  mock_data "aws_iam_openid_connect_provider" {
    defaults = {
      arn = "arn:aws:iam::793680745829:oidc-provider/token.actions.githubusercontent.com"
    }
  }
}

run "state_bucket_is_tls_only" {
  command = plan

  assert {
    condition = anytrue([
      for c in data.aws_iam_policy_document.state_tls_only.statement[0].condition :
      c.test == "Bool" && c.variable == "aws:SecureTransport" && contains(tolist(c.values), "false")
    ]) && data.aws_iam_policy_document.state_tls_only.statement[0].effect == "Deny"
    error_message = "The state bucket policy must deny requests without TLS."
  }
}

# --- Workloads host ---

run "security_group_opens_only_player_ports" {
  command = plan

  assert {
    condition = toset([
      for r in aws_vpc_security_group_ingress_rule.public :
      "${r.ip_protocol}/${r.from_port}-${r.to_port}/${r.cidr_ipv4}"
      ]) == toset([
      "tcp/80-80/0.0.0.0/0",
      "tcp/30000-30000/0.0.0.0/0",
    ]) && length(aws_vpc_security_group_ingress_rule.public) == 2
    error_message = "Ingress must be exactly 80/tcp and 30000/tcp from 0.0.0.0/0: no SSH, no /32 rule."
  }

  assert {
    condition = alltrue([
      for r in aws_vpc_security_group_ingress_rule.public :
      r.cidr_ipv6 == null && r.prefix_list_id == null && r.referenced_security_group_id == null
    ])
    error_message = "Ingress rules must have no other sources."
  }

  assert {
    condition = (
      aws_vpc_security_group_egress_rule.all_ipv4.ip_protocol == "-1" &&
      aws_vpc_security_group_egress_rule.all_ipv4.cidr_ipv4 == "0.0.0.0/0"
    )
    error_message = "The explicit allow-all IPv4 egress rule must exist, or first-boot user data has no network."
  }

  assert {
    condition     = data.aws_subnet.host.availability_zone_id == "usw2-az2" && data.aws_subnet.host.default_for_az
    error_message = "The host subnet must be the default subnet in usw2-az2."
  }
}

run "host_role_is_least_privilege" {
  command = plan

  assert {
    condition     = aws_iam_role_policy_attachments_exclusive.host.policy_arns == toset(["arn:aws:iam::aws:policy/AmazonSSMManagedEC2InstanceDefaultPolicy"])
    error_message = "The host role keeps only the SSM default instance policy, never AmazonSSMManagedInstanceCore."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.host.statement : [
        for a in s.actions : !startswith(lower(a), "kms:") && !strcontains(a, "*")
      ]
    ]))
    error_message = "The host role must have no kms actions and no action wildcards."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.host.statement : [
        for r in s.resources : r != "*"
      ]
    ]))
    error_message = "The host role must grant nothing on *."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.host.statement : [
        for r in s.resources : can(regex("^arn:aws:ssm:us-west-2:793680745829:parameter/foundry(/\\*)?$", r))
      ] if anytrue([for a in s.actions : startswith(a, "ssm:")])
    ]))
    error_message = "ssm:GetParameter* must be scoped to parameter/foundry and parameter/foundry/*."
  }

  assert {
    condition     = one(data.aws_iam_policy_document.host_trust.statement[0].principals).identifiers == toset(["ec2.amazonaws.com"])
    error_message = "The host role must trust only EC2."
  }

  assert {
    condition     = toset(aws_iam_role_policies_exclusive.host.policy_names) == toset(["foundry-host"])
    error_message = "The host role's exclusive inline list must hold only foundry-host."
  }
}

run "host_instance_settings" {
  command = plan

  assert {
    condition     = aws_instance.host.instance_type == "t4g.small" && aws_instance.host.ami == "ami-0baa3cf0d687ee495"
    error_message = "The host is a t4g.small on the committed AL2023 arm64 AMI."
  }

  assert {
    condition = (
      aws_instance.host.metadata_options[0].http_tokens == "required" &&
      aws_instance.host.metadata_options[0].http_put_response_hop_limit == 1
    )
    error_message = "IMDSv2 must be required, with a hop limit of 1."
  }

  assert {
    condition = (
      aws_instance.host.root_block_device[0].encrypted &&
      aws_instance.host.root_block_device[0].volume_type == "gp3" &&
      aws_instance.host.root_block_device[0].volume_size == 10
    )
    error_message = "The root volume must be an encrypted 10 GiB gp3 volume."
  }

  assert {
    condition = alltrue([
      strcontains(aws_instance.host.user_data, "dnf install -y nodejs24 certbot"),
      strcontains(aws_instance.host.user_data, "DATA_DEVICE='/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol0123456789abcdef0'"),
      strcontains(aws_instance.host.user_data, "echo \"$DATA_DEVICE /foundry ext4 defaults,nofail 0 2\" >>/etc/fstab"),
      strcontains(aws_instance.host.user_data, "ExecStart=/usr/bin/node-24 /foundry/foundryvtt/main.js"),
      strcontains(aws_instance.host.user_data, "OnCalendar=*-*-* 10:00:00 UTC"),
      strcontains(aws_instance.host.user_data, "systemctl enable --now certbot-renew.timer"),
      strcontains(aws_instance.host.user_data, "/etc/letsencrypt/renewal-hooks/deploy/foundry.sh"),
      strcontains(aws_instance.host.user_data, "systemctl try-restart foundry.service"),
      strcontains(aws_instance.host.user_data, "\nRestart=on-failure\nRestartSec=5\n"),
    ])
    error_message = "User data must install Node.js 24 and certbot, the fstab entry for the data volume ID, the unit with Restart=on-failure, the timer override and the deploy hook."
  }

  assert {
    condition = !anytrue([
      strcontains(aws_instance.host.user_data, "ExecStartPre"),
      strcontains(aws_instance.host.user_data, "mkfs"),
      strcontains(aws_instance.host.user_data, "enable foundry"),
      strcontains(aws_instance.host.user_data, "enable --now foundry"),
      strcontains(aws_instance.host.user_data, "Restart=no"),
      strcontains(aws_instance.host.user_data, "LABEL=foundry-data /foundry"),
    ])
    error_message = "User data must not keep the preflight line or Restart=no, mount /foundry by label, format a disk, or enable Foundry before the restore."
  }
}

run "data_volume_is_kept_with_the_host" {
  command = plan

  assert {
    condition = (
      aws_ebs_volume.data.size == 20 &&
      aws_ebs_volume.data.type == "gp3" &&
      aws_ebs_volume.data.encrypted &&
      aws_ebs_volume.data.tags["Name"] == "foundry-data"
    )
    error_message = "The data volume must be an encrypted 20 GiB gp3 volume named foundry-data."
  }

  assert {
    condition     = aws_ebs_volume.data.availability_zone == data.aws_subnet.host.availability_zone
    error_message = "The data volume must be in the host's Availability Zone."
  }

  assert {
    condition = (
      aws_volume_attachment.data.device_name == "/dev/sdf" &&
      aws_volume_attachment.data.stop_instance_before_detaching
    )
    error_message = "The data volume attaches on /dev/sdf and detaches only from a stopped instance."
  }
}

run "workloads_snapshot_policy" {
  command = plan

  assert {
    condition = (
      aws_dlm_lifecycle_policy.host.state == "ENABLED" &&
      aws_dlm_lifecycle_policy.host.policy_details[0].resource_types == tolist(["VOLUME"]) &&
      aws_dlm_lifecycle_policy.host.policy_details[0].target_tags == tomap({ project = "foundry" })
    )
    error_message = "The workloads snapshot policy must be enabled and target volumes tagged project=foundry."
  }

  assert {
    condition = jsonencode([
      for s in aws_dlm_lifecycle_policy.host.policy_details[0].schedule : {
        name     = s.name
        copy     = s.copy_tags
        time     = try(s.create_rule[0].times[0], null)
        interval = s.create_rule[0].interval
        cron     = s.create_rule[0].cron_expression
        keep     = s.retain_rule[0].count
      }
      ]) == jsonencode([
      { name = "daily", copy = true, time = "10:00", interval = 24, cron = null, keep = 7 },
      { name = "weekly", copy = true, time = null, interval = null, cron = "cron(0 11 ? * SUN *)", keep = 4 },
    ])
    error_message = "Expected daily 10:00 UTC keeping 7 and weekly Sunday 11:00 UTC keeping 4."
  }

  assert {
    condition = jsonencode({
      for c in data.aws_iam_policy_document.host_dlm_trust.statement[0].condition : c.variable => [c.test, c.values]
      }) == jsonencode({
      "aws:SourceAccount" = ["StringEquals", ["793680745829"]]
      "aws:SourceArn"     = ["ArnLike", ["arn:aws:dlm:us-west-2:793680745829:policy/*"]]
    })
    error_message = "The workloads DLM role must trust only DLM policies in the workloads account."
  }
}

run "status_alarm_targets_workloads_alerts" {
  command = plan

  assert {
    condition = (
      aws_cloudwatch_metric_alarm.host_status_check.metric_name == "StatusCheckFailed" &&
      aws_cloudwatch_metric_alarm.host_status_check.alarm_actions == toset(["arn:aws:sns:us-west-2:793680745829:alerts"])
    )
    error_message = "D18: the StatusCheckFailed alarm notifies the workloads alerts topic."
  }
}

# --- GitHub Actions host control ---

run "host_control_role_trusts_only_the_environment" {
  command = plan

  assert {
    condition = jsonencode({
      for c in data.aws_iam_policy_document.host_control_trust.statement[0].condition : c.variable => [c.test, c.values]
      }) == jsonencode({
      "token.actions.githubusercontent.com:aud" = ["StringEquals", ["sts.amazonaws.com"]]
      "token.actions.githubusercontent.com:sub" = ["StringEquals", ["repo:CraigDevJohnson/foundry:environment:foundry-host"]]
    })
    error_message = "The host control role must require the STS audience and pin sub to the foundry-host environment."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.host_control.statement :
      s.resources == toset(["*"]) ? s.actions == toset(["ec2:DescribeInstances"]) || s.actions == toset(["ssm:GetCommandInvocation"]) : true
    ])
    error_message = "Only the read actions without resource-level permissions may use a * resource."
  }

  assert {
    condition = alltrue([
      for r in flatten([for s in data.aws_iam_policy_document.host_control.statement : tolist(s.resources) if contains(tolist(s.actions), "ssm:SendCommand")]) :
      !strcontains(r, ":document/") || r == "arn:aws:ssm:us-west-2:793680745829:document/foundry-shutdown"
    ])
    error_message = "SendCommand may use only the foundry-shutdown document, never AWS-RunShellScript."
  }

  assert {
    condition = (
      aws_ssm_document.shutdown.document_type == "Command" &&
      !strcontains(aws_ssm_document.shutdown.content, "\"parameters\"")
    )
    error_message = "foundry-shutdown must be a Command document with no parameters."
  }
}
