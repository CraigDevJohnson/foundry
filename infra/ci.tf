# The GitHub Actions role that starts and stops the host (aws-design §5.6):
# the Host control workflow assumes it through the workloads GitHub OIDC
# provider, which aws-setup owns. It can start and stop only this instance and
# run commands on it only through AWS-RunShellScript.

locals {
  github_repository = "CraigDevJohnson/foundry"

  # The workflow's GitHub Environment. The trust policy pins `sub` to it, so
  # only jobs in that environment can assume the role, and its protection
  # rules (required reviewers, deployment branches) apply to every run.
  host_control_environment = "foundry-host"

  # The AWS managed document; it has no owner account in its ARN.
  run_shell_script_document_arn = "arn:aws:ssm:${local.region}::document/AWS-RunShellScript"
}

data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

data "aws_iam_policy_document" "host_control_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${local.github_repository}:environment:${local.host_control_environment}"]
    }
  }
}

resource "aws_iam_role" "host_control" {
  name                  = "foundry-host-control-ci"
  description           = "GitHub Actions: start, stop and shut down Foundry on the host"
  assume_role_policy    = data.aws_iam_policy_document.host_control_trust.json
  max_session_duration  = 3600
  force_detach_policies = true
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
      local.run_shell_script_document_arn,
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
  role   = aws_iam_role.host_control.id
  policy = data.aws_iam_policy_document.host_control.json
}

resource "aws_iam_role_policies_exclusive" "host_control" {
  role_name    = aws_iam_role.host_control.name
  policy_names = [aws_iam_role_policy.host_control.name]
}

resource "aws_iam_role_policy_attachments_exclusive" "host_control" {
  role_name   = aws_iam_role.host_control.name
  policy_arns = []
}
