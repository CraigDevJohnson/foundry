# What the GitHub Actions role that starts and stops the host may do
# (aws-design §5.6). The Host control workflow assumes foundry-host-control-ci
# through the workloads GitHub OIDC provider, which aws-setup owns, and only
# from this repository's foundry-host environment. infra/bootstrap makes the
# role and owns that trust (ADR 0007); this root sets its inline policy. It can
# start and stop only this instance, and the only command it can run there is
# the parameterless foundry-shutdown document, so it can't run arbitrary shell
# as root on the host.

locals {
  host_control_role_name = "foundry-host-control-ci"
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
    sid     = "ShutDownFoundry"
    actions = ["ssm:SendCommand"]
    resources = [
      aws_instance.host.arn,
      aws_ssm_document.shutdown.arn,
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
