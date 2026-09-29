# Snapshot policy for the workloads host (aws-design §6.2 and §9.3): both
# volumes, daily at 10:00 UTC keeping 7 and weekly on Sunday at 11:00 UTC
# keeping 4, the same as the interim management policy. The addresses differ
# from mgmt_interim.tf so both can exist until Phase 9 removes the interim one.

locals {
  host_snapshot_target_tag = {
    key   = "project"
    value = "foundry"
  }
}

data "aws_iam_policy_document" "host_dlm_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.workloads_account_id]
    }

    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:dlm:${local.region}:${local.workloads_account_id}:policy/*"]
    }
  }
}

resource "aws_iam_role" "host_dlm" {
  name               = "foundry-dlm"
  description        = "Data Lifecycle Manager execution role for the Foundry snapshot policy"
  assume_role_policy = data.aws_iam_policy_document.host_dlm_trust.json
}

resource "aws_iam_role_policy_attachment" "host_dlm" {
  role       = aws_iam_role.host_dlm.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}

resource "aws_dlm_lifecycle_policy" "host" {
  description        = "Foundry daily and weekly EBS snapshots"
  execution_role_arn = aws_iam_role.host_dlm.arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags = {
      (local.host_snapshot_target_tag.key) = local.host_snapshot_target_tag.value
    }

    schedule {
      name      = "daily"
      copy_tags = true

      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["10:00"]
      }

      retain_rule {
        count = 7
      }
    }

    schedule {
      name      = "weekly"
      copy_tags = true

      create_rule {
        cron_expression = "cron(0 11 ? * SUN *)"
      }

      retain_rule {
        count = 4
      }
    }
  }

  depends_on = [aws_iam_role_policy_attachment.host_dlm]
}
