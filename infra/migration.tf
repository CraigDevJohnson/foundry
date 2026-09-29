# TEMPORARY: the transfer path from the management host to the workloads host
# (refactor Phases 7-8). Snapshots under the AWS managed aws/ebs key can't be
# shared across accounts, so the move is a file-level copy through this bucket.
#
# Phase 9 deletes this whole file, together with mgmt_interim.tf, and applies.
# Nothing outside those two files refers to anything here.

locals {
  migration_bucket     = "foundry-migration-${local.workloads_account_id}"
  migration_bucket_arn = "arn:aws:s3:::${local.migration_bucket}"
  migration_prefix     = "transfer/"

  # The management host's instance role (imported in mgmt_interim.tf).
  migration_source_role_arn = "arn:aws:iam::${local.management_account_id}:role/foundry"
}

resource "aws_s3_bucket" "migration" {
  bucket = local.migration_bucket

  # Holds only copies; Phase 9 deletes the bucket with whatever is left.
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "migration" {
  bucket = aws_s3_bucket.migration.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "migration" {
  bucket = aws_s3_bucket.migration.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "migration" {
  bucket = aws_s3_bucket.migration.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "migration" {
  bucket = aws_s3_bucket.migration.id

  rule {
    id     = "expire-transfer-copies"
    status = "Enabled"

    filter {}

    expiration {
      days = 7
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

data "aws_iam_policy_document" "migration_bucket" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      local.migration_bucket_arn,
      "${local.migration_bucket_arn}/*",
    ]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  statement {
    sid = "ManagementHostUpload"
    actions = [
      "s3:PutObject",
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
    ]
    resources = ["${local.migration_bucket_arn}/${local.migration_prefix}*"]

    principals {
      type        = "AWS"
      identifiers = [local.migration_source_role_arn]
    }
  }

  statement {
    sid       = "ManagementHostList"
    actions   = ["s3:ListBucket"]
    resources = [local.migration_bucket_arn]

    principals {
      type        = "AWS"
      identifiers = [local.migration_source_role_arn]
    }
  }
}

resource "aws_s3_bucket_policy" "migration" {
  bucket = aws_s3_bucket.migration.id
  policy = data.aws_iam_policy_document.migration_bucket.json

  # Avoid concurrent bucket-level S3 calls on the same bucket.
  depends_on = [
    aws_s3_bucket_public_access_block.migration,
    aws_s3_bucket_ownership_controls.migration,
  ]
}

# --- Workloads host: read the transfer copies ---

data "aws_iam_policy_document" "host_migration" {
  statement {
    sid       = "ReadTransferCopies"
    actions   = ["s3:GetObject"]
    resources = ["${local.migration_bucket_arn}/${local.migration_prefix}*"]
  }

  statement {
    sid       = "ListTransferBucket"
    actions   = ["s3:ListBucket"]
    resources = [local.migration_bucket_arn]
  }
}

resource "aws_iam_role_policy" "host_migration" {
  name   = "foundry-migration-get"
  role   = aws_iam_role.host.id
  policy = data.aws_iam_policy_document.host_migration.json
}

# --- Management host: upload the transfer copies ---

data "aws_iam_policy_document" "foundry_migration_put" {
  provider = aws.mgmt

  statement {
    sid = "UploadTransferCopies"
    actions = [
      "s3:PutObject",
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
    ]
    resources = ["${local.migration_bucket_arn}/${local.migration_prefix}*"]
  }

  statement {
    sid       = "ListTransferBucket"
    actions   = ["s3:ListBucket"]
    resources = [local.migration_bucket_arn]
  }
}

# Listed in aws_iam_role_policies_exclusive.foundry (mgmt_interim.tf).
resource "aws_iam_role_policy" "foundry_migration_put" {
  provider = aws.mgmt

  name   = "foundry-migration-put"
  role   = aws_iam_role.foundry.id
  policy = data.aws_iam_policy_document.foundry_migration_put.json
}
