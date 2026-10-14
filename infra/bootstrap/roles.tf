# The roles whose inline policies the infra root sets: the two controllers'
# execution roles and the Host control workflow's role. They are made here, with Craig's admin sign-in, and not by the deploy
# role (ADR 0007). IAM has no condition key on a trust policy, so a deploy
# role that could make a role, or change who may assume one, could make one
# that an outside account assumes after its run has ended. The infra root
# sets only their inline policies, which the boundary caps.

locals {
  # The Lobby's site environments: one controller each (infra/controller.tf).
  site_environments = toset(["dev", "prod"])

  controller_role_names = {
    for environment in local.site_environments : environment => "foundry-${environment}-controller"
  }

  host_control_role_name = "foundry-host-control-ci"

  # The Host control workflow's GitHub Environment. The trust policy pins
  # `sub` to it, so only jobs in that environment can assume the role, and
  # its protection rules (deployment branch main) apply to every run.
  host_control_environment = "foundry-host"
}

# Only Lambda may assume a controller role: each role trusts only its service
# principal (aws-design §5.3). Like soccer's and The Lobby's function roles,
# and unlike the DLM role, it has no aws:SourceAccount condition.
data "aws_iam_policy_document" "controller_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "controller" {
  for_each = local.site_environments

  name                  = local.controller_role_names[each.key]
  description           = "Foundry controller for The Lobby ${each.key} site - host status, start and stop"
  assume_role_policy    = data.aws_iam_policy_document.controller_trust.json
  force_detach_policies = true
  permissions_boundary  = aws_iam_policy.boundary.arn
}

# The Host control workflow assumes this role through the workloads GitHub
# OIDC provider, which aws-setup owns (aws-design §5.6).
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

# The infra root made this role before the deploy role existed. Its first
# Deploy run forgets it (infra/ci.tf), and this root adopts it. The offline
# tests turn the import off: `tofu test` can't import.
variable "adopt_host_control_role" {
  description = "Import the existing foundry-host-control-ci role. Only the offline tests set this to false."
  type        = bool
  default     = true
}

import {
  for_each = var.adopt_host_control_role ? toset([local.host_control_role_name]) : toset([])
  to       = aws_iam_role.host_control
  id       = each.value
}

resource "aws_iam_role" "host_control" {
  name                  = local.host_control_role_name
  description           = "GitHub Actions: start, stop and shut down Foundry on the host"
  assume_role_policy    = data.aws_iam_policy_document.host_control_trust.json
  max_session_duration  = 3600
  force_detach_policies = true
  permissions_boundary  = aws_iam_policy.boundary.arn
}
