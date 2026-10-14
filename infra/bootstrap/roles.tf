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

# The roles may already exist. The infra root made foundry-host-control-ci
# before this root did, and an apply of the infra root from before the deploy
# role would have made the controllers' roles. So this root adopts whichever
# of the three exist and makes the rest, and the infra root forgets them
# (infra/ci.tf, infra/controller.tf). The offline tests mock this list empty:
# `tofu test` can't import.
data "aws_iam_roles" "existing" {
  name_regex = "^foundry-(dev-controller|prod-controller|host-control-ci)$"
}

import {
  for_each = {
    for environment, name in local.controller_role_names : environment => name
    if contains(data.aws_iam_roles.existing.names, name)
  }
  to = aws_iam_role.controller[each.key]
  id = each.value
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

import {
  for_each = contains(data.aws_iam_roles.existing.names, local.host_control_role_name) ? toset([local.host_control_role_name]) : toset([])
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
