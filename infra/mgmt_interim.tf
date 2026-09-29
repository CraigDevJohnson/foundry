# Interim protection for the hand-built host i-01b0381fb22db508f, which still
# runs in the management account (refactor Phase 3, aws-design §13 fact 2).
#
# Every resource here uses the aws.mgmt provider. After the workloads host is
# built and verified (Phases 7-8), Phase 9 removes this whole file and applies,
# which deletes the interim snapshot policy, its role and the old host role.

locals {
  # The two volumes of the current host. Both already carried project=foundry
  # in the 2026-09-28 read-only audit. The tags are not managed here; the
  # snapshot policy's precondition only checks them.
  interim_volume_ids = {
    root = "vol-005433f97d33e3c96"
    data = "vol-0c95953206040fa9c"
  }

  snapshot_target_tag = {
    key   = "project"
    value = "foundry"
  }
}

# --- Snapshot policy (Data Lifecycle Manager) ---

# Read-only guard: which of the two volumes carry the snapshot target tag.
data "aws_ebs_volumes" "interim_tagged" {
  provider = aws.mgmt

  filter {
    name   = "volume-id"
    values = values(local.interim_volume_ids)
  }

  tags = {
    (local.snapshot_target_tag.key) = local.snapshot_target_tag.value
  }
}

data "aws_iam_policy_document" "dlm_trust" {
  provider = aws.mgmt

  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.management_account_id]
    }

    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:dlm:${local.region}:${local.management_account_id}:policy/*"]
    }
  }
}

resource "aws_iam_role" "dlm" {
  provider = aws.mgmt

  name               = "foundry-dlm"
  description        = "Data Lifecycle Manager execution role for the interim Foundry snapshot policy"
  assume_role_policy = data.aws_iam_policy_document.dlm_trust.json
}

resource "aws_iam_role_policy_attachment" "dlm" {
  provider = aws.mgmt

  role       = aws_iam_role.dlm.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}

# Daily at 10:00 UTC keeping 7, and weekly on Sunday at 11:00 UTC keeping 4,
# on both volumes (aws-design §6.2). Take a manual snapshot before upgrades.
resource "aws_dlm_lifecycle_policy" "interim" {
  provider = aws.mgmt

  description        = "Foundry interim daily and weekly EBS snapshots"
  execution_role_arn = aws_iam_role.dlm.arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags = {
      (local.snapshot_target_tag.key) = local.snapshot_target_tag.value
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

  depends_on = [aws_iam_role_policy_attachment.dlm]

  lifecycle {
    # Fail the plan instead of letting DLM silently skip an untagged volume.
    precondition {
      condition     = length(setsubtract(values(local.interim_volume_ids), data.aws_ebs_volumes.interim_tagged.ids)) == 0
      error_message = "Every interim Foundry volume must carry ${local.snapshot_target_tag.key}=${local.snapshot_target_tag.value} before the snapshot policy is applied. Missing: ${join(", ", setsubtract(values(local.interim_volume_ids), data.aws_ebs_volumes.interim_tagged.ids))}."
    }
  }
}

# --- Host role (least privilege, decisions D6 and D7) ---

import {
  to = aws_iam_role.foundry
  id = "foundry"
}

data "aws_iam_policy_document" "foundry_trust" {
  provider = aws.mgmt

  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "foundry" {
  provider = aws.mgmt

  name               = "foundry"
  description        = "Allows EC2 instances to call AWS services on your behalf."
  assume_role_policy = data.aws_iam_policy_document.foundry_trust.json

  # Lets Phase 9 delete the role even if something attached a policy by hand.
  force_detach_policies = true
}

import {
  to = aws_iam_instance_profile.foundry
  id = "foundry"
}

resource "aws_iam_instance_profile" "foundry" {
  provider = aws.mgmt

  name = "foundry"
  role = aws_iam_role.foundry.name
}

locals {
  foundry_ssm_policy_arn              = "arn:aws:iam::aws:policy/AmazonSSMManagedEC2InstanceDefaultPolicy"
  foundry_cloudwatch_agent_policy_arn = "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy"

  # Never AmazonSSMManagedInstanceCore: it grants ssm:GetParameter(s) on *, and
  # the account's aws/ssm key would let the host read every app's SecureStrings.
  foundry_managed_policy_arns = concat(
    [local.foundry_ssm_policy_arn],
    var.keep_cloudwatch_agent ? [local.foundry_cloudwatch_agent_policy_arn] : [],
  )
}

resource "aws_iam_role_policy_attachment" "foundry" {
  provider = aws.mgmt
  for_each = toset(local.foundry_managed_policy_arns)

  role       = aws_iam_role.foundry.name
  policy_arn = each.value
}

# Detaches every managed policy not listed above, which removes
# AmazonSSMManagedInstanceCore (and CloudWatchAgentServerPolicy unless D7 keeps
# it). It runs after the new attachment so the SSM agent keeps working.
resource "aws_iam_role_policy_attachments_exclusive" "foundry" {
  provider = aws.mgmt

  role_name   = aws_iam_role.foundry.name
  policy_arns = local.foundry_managed_policy_arns

  depends_on = [aws_iam_role_policy_attachment.foundry]
}

# D6: no inline policies, which deletes foundry-server-role and its
# ssm:GetParameter*, kms:Decrypt and kms:CreateKey grants on *.
# TODO(phase 2): if the host API-usage read shows the host needs another API,
# add one aws_iam_role_policy scoped to that resource and list its name here.
# Never ssm:GetParameter* or kms:* on *.
resource "aws_iam_role_policies_exclusive" "foundry" {
  provider = aws.mgmt

  role_name    = aws_iam_role.foundry.name
  policy_names = []

  depends_on = [aws_iam_role_policy_attachment.foundry]
}
