# Offline plan checks. Both providers are mocked, so no AWS credentials or
# calls are needed: run `tofu -chdir=infra test` after `init -backend=false`.

mock_provider "aws" {
  mock_resource "aws_s3_bucket" {
    defaults = {
      arn = "arn:aws:s3:::foundry-tofu-state-793680745829"
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }
}

mock_provider "aws" {
  alias = "mgmt"

  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::180294223248:role/mock"
    }
  }

  mock_data "aws_ebs_volumes" {
    defaults = {
      ids = ["vol-005433f97d33e3c96", "vol-0c95953206040fa9c"]
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }
}

run "snapshot_policy_covers_both_volumes" {
  command = plan

  override_data {
    target = data.aws_ebs_volumes.interim_tagged
    values = {
      ids = ["vol-005433f97d33e3c96", "vol-0c95953206040fa9c"]
    }
  }

  assert {
    condition     = aws_dlm_lifecycle_policy.interim.policy_details[0].resource_types == tolist(["VOLUME"])
    error_message = "The snapshot policy must target volumes."
  }

  assert {
    condition     = aws_dlm_lifecycle_policy.interim.policy_details[0].target_tags == tomap({ project = "foundry" })
    error_message = "The snapshot policy must target project=foundry."
  }

  assert {
    condition = jsonencode([
      for s in aws_dlm_lifecycle_policy.interim.policy_details[0].schedule : {
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
}

run "dlm_role_is_scoped_to_this_account" {
  command = plan

  assert {
    condition = jsonencode({
      for c in data.aws_iam_policy_document.dlm_trust.statement[0].condition : c.variable => [c.test, c.values]
      }) == jsonencode({
      "aws:SourceAccount" = ["StringEquals", ["180294223248"]]
      "aws:SourceArn"     = ["ArnLike", ["arn:aws:dlm:us-west-2:180294223248:policy/*"]]
    })
    error_message = "The DLM role must trust only DLM policies in the management account."
  }
}

run "host_role_is_least_privilege_by_default" {
  command = plan

  assert {
    condition     = aws_iam_role_policy_attachments_exclusive.foundry.policy_arns == toset(["arn:aws:iam::aws:policy/AmazonSSMManagedEC2InstanceDefaultPolicy"])
    error_message = "By default (D7) the host role keeps only the SSM default instance policy."
  }

  assert {
    condition     = length(aws_iam_role_policies_exclusive.foundry.policy_names) == 0
    error_message = "D6: the host role must have no inline policies."
  }

  assert {
    condition     = aws_iam_role.foundry.force_detach_policies
    error_message = "The imported host role must force-detach policies on delete."
  }
}

run "cloudwatch_agent_can_be_kept" {
  command = plan

  variables {
    keep_cloudwatch_agent = true
  }

  assert {
    condition = aws_iam_role_policy_attachments_exclusive.foundry.policy_arns == toset([
      "arn:aws:iam::aws:policy/AmazonSSMManagedEC2InstanceDefaultPolicy",
      "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy",
    ])
    error_message = "keep_cloudwatch_agent must add only CloudWatchAgentServerPolicy."
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

run "untagged_volume_fails_the_plan" {
  command = plan

  override_data {
    target = data.aws_ebs_volumes.interim_tagged
    values = {
      ids = ["vol-005433f97d33e3c96"]
    }
  }

  expect_failures = [aws_dlm_lifecycle_policy.interim]
}
