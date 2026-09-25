locals {
  oregon_image_arns       = "arn:aws:ec2:us-west-2::image/*"
  oregon_snapshot_arns    = "arn:aws:ec2:us-west-2::snapshot/*"
  ohio_image_arns         = "arn:aws:ec2:us-east-2::image/*"
  ohio_snapshot_arns      = "arn:aws:ec2:us-east-2::snapshot/*"
  generated_image_arns    = [local.oregon_image_arns, local.ohio_image_arns]
  generated_snapshot_arns = [local.oregon_snapshot_arns, local.ohio_snapshot_arns]
  ebs_key_arns            = [var.oregon_ebs_key_arn, var.ohio_ebs_key_arn]

  backup_permissions_json = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "DescribeEC2SourceAndBackupArtifacts"
        Effect   = "Allow"
        Action   = ["ec2:DescribeImages", "ec2:DescribeInstances", "ec2:DescribeInstanceAttribute", "ec2:DescribeInstanceCreditSpecifications", "ec2:DescribeNetworkInterfaces", "ec2:DescribeElasticGpus", "ec2:DescribeSpotInstanceRequests", "ec2:DescribeSnapshots", "ec2:DescribeSnapshotTierStatus", "ec2:DescribeTags", "ec2:DescribeVolumes"]
        Resource = "*"
        Condition = {
          StringEquals = { "aws:RequestedRegion" = ["us-west-2", "us-east-2"] }
        }
      },
      {
        Sid      = "ReadSourceTags"
        Effect   = "Allow"
        Action   = "tag:GetResources"
        Resource = "*"
        Condition = {
          StringEquals = { "aws:RequestedRegion" = ["us-west-2", "us-east-2"] }
        }
      },
      {
        Sid      = "CreateImageFromOnlyFoundry"
        Effect   = "Allow"
        Action   = "ec2:CreateImage"
        Resource = [var.source_instance_arn, local.oregon_image_arns, local.oregon_snapshot_arns]
      },
      {
        Sid      = "CreateSourceVolumeSnapshots"
        Effect   = "Allow"
        Action   = "ec2:CreateSnapshot"
        Resource = concat(tolist(var.source_volume_arns), [local.oregon_snapshot_arns])
      },
      {
        Sid      = "CopyTrialImagesAndSnapshots"
        Effect   = "Allow"
        Action   = ["ec2:CopyImage", "ec2:CopySnapshot"]
        Resource = concat(local.generated_image_arns, local.generated_snapshot_arns)
      },
      {
        Sid      = "TagTrialImagesAndSnapshots"
        Effect   = "Allow"
        Action   = "ec2:CreateTags"
        Resource = concat(local.generated_image_arns, local.generated_snapshot_arns)
      },
      {
        Sid      = "ManageTaggedTrialImages"
        Effect   = "Allow"
        Action   = ["ec2:DeregisterImage", "ec2:ModifyImageAttribute"]
        Resource = local.generated_image_arns
        Condition = {
          StringEquals = { "aws:ResourceTag/TrialId" = var.trial_id }
        }
      },
      {
        Sid      = "ModifyBackupSnapshots"
        Effect   = "Allow"
        Action   = "ec2:ModifySnapshotAttribute"
        Resource = local.generated_snapshot_arns
        Condition = {
          Null = { "aws:ResourceTag/aws:backup:source-resource" = "false" }
        }
      },
      {
        Sid      = "ExpireBackupSnapshots"
        Effect   = "Allow"
        Action   = "ec2:DeleteSnapshot"
        Resource = local.generated_snapshot_arns
      },
      {
        Sid      = "DescribeTrialVaults"
        Effect   = "Allow"
        Action   = "backup:DescribeBackupVault"
        Resource = [local.oregon_vault_arn, local.ohio_vault_arn]
      },
      {
        Sid      = "CopyIntoOnlyOhioTrialVault"
        Effect   = "Allow"
        Action   = "backup:CopyIntoBackupVault"
        Resource = local.ohio_vault_arn
      },
      {
        Sid      = "CopyOnlyTaggedOregonImageRecoveryPoints"
        Effect   = "Allow"
        Action   = "backup:CopyFromBackupVault"
        Resource = local.oregon_image_arns
        Condition = {
          StringEquals = { "aws:ResourceTag/TrialId" = var.trial_id }
        }
      },
      {
        Sid      = "DescribeOnlyEBSKeys"
        Effect   = "Allow"
        Action   = "kms:DescribeKey"
        Resource = local.ebs_key_arns
      },
      {
        Sid      = "DescribeOregonBackupKeyForCrossRegionCopy"
        Effect   = "Allow"
        Action   = "kms:DescribeKey"
        Resource = var.oregon_backup_key_arn
        Condition = {
          "ForAnyValue:StringEquals" = { "kms:ResourceAliases" = "alias/aws/backup" }
        }
      },
      {
        Sid      = "GrantOnlyEBSKeysForAWSResources"
        Effect   = "Allow"
        Action   = "kms:CreateGrant"
        Resource = local.ebs_key_arns
        Condition = {
          Bool = { "kms:GrantIsForAWSResource" = "true" }
        }
      },
      {
        Sid      = "UseOnlyEBSKeysThroughEC2"
        Effect   = "Allow"
        Action   = ["kms:Decrypt", "kms:GenerateDataKey", "kms:GenerateDataKeyWithoutPlaintext", "kms:ReEncryptTo", "kms:ReEncryptFrom"]
        Resource = local.ebs_key_arns
        Condition = {
          StringEquals               = { "kms:ViaService" = ["ec2.us-west-2.amazonaws.com", "ec2.us-east-2.amazonaws.com"] }
          "ForAnyValue:StringEquals" = { "kms:EncryptionContextKeys" = "aws:ebs:id" }
          Null                       = { "kms:EncryptionContextKeys" = "false" }
        }
      },
    ]
  })
}

resource "aws_iam_policy" "backup_boundary" {
  provider    = aws.oregon
  name        = "${local.vault_prefix}-AWSBackup-boundary"
  description = "Maximum EC2/EBS backup and copy permissions for the Foundry measurement trial"
  policy      = local.backup_permissions_json
  tags        = local.tags
}

resource "aws_iam_role" "backup" {
  provider             = aws.oregon
  name                 = "${local.vault_prefix}-AWSBackup"
  assume_role_policy   = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = "sts:AssumeRole", Principal = { Service = "backup.amazonaws.com" } }] })
  permissions_boundary = aws_iam_policy.backup_boundary.arn
  tags                 = local.tags
}

resource "aws_iam_role_policy" "backup" {
  provider = aws.oregon
  name     = "${local.vault_prefix}-backup-copy"
  role     = aws_iam_role.backup.id
  policy   = local.backup_permissions_json
}
