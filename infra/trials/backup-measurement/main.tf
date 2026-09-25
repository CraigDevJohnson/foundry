locals {
  vault_prefix      = "foundry-bm-${var.trial_id}"
  oregon_vault_name = "${local.vault_prefix}-us-west-2"
  ohio_vault_name   = "${local.vault_prefix}-us-east-2"
  oregon_vault_arn  = "arn:aws:backup:us-west-2:${var.account_id}:backup-vault:${local.oregon_vault_name}"
  ohio_vault_arn    = "arn:aws:backup:us-east-2:${var.account_id}:backup-vault:${local.ohio_vault_name}"
  tags = {
    ManagedBy = "terraform"
    Project   = "foundry"
    Purpose   = "backup-measurement"
    TrialId   = var.trial_id
  }
}

resource "aws_backup_vault" "oregon" {
  provider      = aws.oregon
  name          = local.oregon_vault_name
  kms_key_arn   = var.oregon_ebs_key_arn
  force_destroy = false
  tags          = local.tags

  depends_on = [aws_iam_role_policy.backup]
}

resource "aws_backup_vault_lock_configuration" "oregon" {
  provider           = aws.oregon
  backup_vault_name  = aws_backup_vault.oregon.name
  min_retention_days = 7
  max_retention_days = 7
}

resource "aws_backup_vault" "ohio" {
  provider      = aws.ohio
  name          = local.ohio_vault_name
  kms_key_arn   = var.ohio_ebs_key_arn
  force_destroy = false
  tags          = local.tags

  depends_on = [aws_iam_role_policy.backup]
}

resource "aws_backup_vault_lock_configuration" "ohio" {
  provider           = aws.ohio
  backup_vault_name  = aws_backup_vault.ohio.name
  min_retention_days = 7
  max_retention_days = 7
}
