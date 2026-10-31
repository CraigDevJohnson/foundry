# What the GitHub Actions role that starts and stops the host may do
# (aws-design §5.6). The Host control workflow assumes foundry-host-control-ci
# through the workloads GitHub OIDC provider, which aws-setup owns, and only
# from this repository's foundry-host environment. infra/bootstrap makes the
# role and owns that trust (ADR 0007); this root sets its inline policy. It can
# start and stop only this instance, and the only commands it can run there
# are the parameterless foundry-shutdown and foundry-proxy-setup documents, so
# it can't run arbitrary shell as root on the host.

locals {
  host_control_role_name = "foundry-host-control-ci"

  # The proxy document installs this commit's two proxy scripts on the host
  # and runs the setup (ADR 0008).
  proxy_apply_script = templatefile("${path.module}/templates/foundry-proxy-apply.sh.tftpl", {
    proxy_setup = file("${path.module}/templates/foundry-proxy-setup.sh")
    cert_deploy = file("${path.module}/templates/foundry-cert-deploy.sh")
  })
}

# This root made the role before the deploy role existed. infra/bootstrap now
# owns it, so the first Deploy run forgets it here without deleting it.
removed {
  from = aws_iam_role.host_control

  lifecycle {
    destroy = false
  }
}

# Runs templates/foundry-shutdown.sh as root. It has no parameters, so
# SendCommand callers can't change what runs.
resource "aws_ssm_document" "shutdown" {
  name            = "foundry-shutdown"
  document_type   = "Command"
  document_format = "JSON"

  content = jsonencode({
    schemaVersion = "2.2"
    description   = "Shut Foundry down with /foundry/stop_foundry.sh and systemd before the host stops"
    mainSteps = [{
      action = "aws:runShellScript"
      name   = "shutdownFoundry"
      inputs = {
        timeoutSeconds = "300"
        runCommand     = [file("${path.module}/templates/foundry-shutdown.sh")]
      }
    }]
  })
}

# Sets up the reverse proxy on the host (ADR 0008) and brings its copies of
# the proxy scripts up to date: user data runs only at first boot, so this is
# how the proxy reached the host built before it, and how a change to the
# scripts reaches a built host. It has no parameters, so SendCommand callers
# can't change what runs. Foundry restarts only if its options change.
resource "aws_ssm_document" "proxy_setup" {
  name            = "foundry-proxy-setup"
  document_type   = "Command"
  document_format = "JSON"

  content = jsonencode({
    schemaVersion = "2.2"
    description   = "Install the reverse proxy scripts, set nginx up for https://foundry.craigdevjohnson.com and point Foundry at it"
    mainSteps = [{
      action = "aws:runShellScript"
      name   = "setUpProxy"
      inputs = {
        timeoutSeconds = "900"
        runCommand     = [local.proxy_apply_script]
      }
    }]
  })

  lifecycle {
    precondition {
      # The document writes each script out through a quoted heredoc that
      # ends at that line.
      condition = alltrue([
        for script, terminator in {
          "templates/foundry-proxy-setup.sh" = "FOUNDRY_PROXY_SETUP"
          "templates/foundry-cert-deploy.sh" = "FOUNDRY_CERT_DEPLOY"
        } :
        endswith(file("${path.module}/${script}"), "\n") && !can(regex("(?m)^${terminator}$", file("${path.module}/${script}")))
      ])
      error_message = "templates/foundry-proxy-setup.sh and templates/foundry-cert-deploy.sh must end with a newline and never contain the line FOUNDRY_PROXY_SETUP or FOUNDRY_CERT_DEPLOY: templates/foundry-proxy-apply.sh.tftpl ends its heredocs there."
    }
  }
}

data "aws_iam_policy_document" "host_control" {
  # Describe* has no resource-level permissions. The tasks find the host by
  # its Name and project tags.
  statement {
    sid       = "FindHost"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"]
  }

  statement {
    sid = "StartStopHost"
    actions = [
      "ec2:StartInstances",
      "ec2:StopInstances",
    ]
    resources = [aws_instance.host.arn]
  }

  # SendCommand needs both the target instance and the document.
  statement {
    sid     = "RunFoundryDocuments"
    actions = ["ssm:SendCommand"]
    resources = [
      aws_instance.host.arn,
      aws_ssm_document.shutdown.arn,
      aws_ssm_document.proxy_setup.arn,
    ]
  }

  # GetCommandInvocation has no resource-level permissions.
  statement {
    sid       = "ReadCommandResult"
    actions   = ["ssm:GetCommandInvocation"]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "host_control" {
  name   = "foundry-host-control"
  role   = local.host_control_role_name
  policy = data.aws_iam_policy_document.host_control.json
}

resource "aws_iam_role_policies_exclusive" "host_control" {
  role_name    = local.host_control_role_name
  policy_names = [aws_iam_role_policy.host_control.name]
}

resource "aws_iam_role_policy_attachments_exclusive" "host_control" {
  role_name   = local.host_control_role_name
  policy_arns = []
}
