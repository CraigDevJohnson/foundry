# The state bucket is bootstrapped by CLI (create-bucket, versioning, tagging)
# and then adopted here. New buckets already have Block Public Access, bucket
# owner enforced and SSE-S3, so those imports should plan as no-ops.

locals {
  state_bucket = "foundry-tofu-state-${local.workloads_account_id}"
}

import {
  to = aws_s3_bucket.state
  id = local.state_bucket
}

resource "aws_s3_bucket" "state" {
  bucket = local.state_bucket

  lifecycle {
    prevent_destroy = true
  }
}

import {
  to = aws_s3_bucket_versioning.state
  id = local.state_bucket
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id

  versioning_configuration {
    status = "Enabled"
  }
}

import {
  to = aws_s3_bucket_public_access_block.state
  id = local.state_bucket
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket = aws_s3_bucket.state.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

import {
  to = aws_s3_bucket_ownership_controls.state
  id = local.state_bucket
}

resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

import {
  to = aws_s3_bucket_server_side_encryption_configuration.state
  id = local.state_bucket
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

data "aws_iam_policy_document" "state_tls_only" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.state.arn,
      "${aws_s3_bucket.state.arn}/*",
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
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = data.aws_iam_policy_document.state_tls_only.json

  # Avoid concurrent bucket-level S3 calls on the same bucket.
  depends_on = [aws_s3_bucket_public_access_block.state]
}
