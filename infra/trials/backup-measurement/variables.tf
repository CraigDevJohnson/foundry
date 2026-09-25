variable "account_id" {
  description = "Account approved for this isolated Foundry measurement trial."
  type        = string
  default     = "180294223248"

  validation {
    condition     = var.account_id == "180294223248"
    error_message = "This trial is restricted to management account 180294223248."
  }
}

variable "trial_id" {
  description = "Fixed identifier for this one approved measurement candidate."
  type        = string
  default     = "i13-20260924"

  validation {
    condition     = var.trial_id == "i13-20260924"
    error_message = "This root is reserved for TrialId i13-20260924."
  }
}

variable "source_instance_arn" {
  description = "Exact existing Oregon Foundry EC2 instance ARN approved as the backup source."
  type        = string
  default     = "arn:aws:ec2:us-west-2:180294223248:instance/i-01b0381fb22db508f"

  validation {
    condition     = var.source_instance_arn == "arn:aws:ec2:us-west-2:180294223248:instance/i-01b0381fb22db508f"
    error_message = "This trial is restricted to the reviewed Foundry instance."
  }
}

variable "source_volume_arns" {
  description = "Exact EBS volume ARNs attached to the approved source instance."
  type        = set(string)
  default = [
    "arn:aws:ec2:us-west-2:180294223248:volume/vol-005433f97d33e3c96",
    "arn:aws:ec2:us-west-2:180294223248:volume/vol-0c95953206040fa9c",
  ]

  validation {
    condition = length(var.source_volume_arns) == 2 && alltrue([
      contains(var.source_volume_arns, "arn:aws:ec2:us-west-2:180294223248:volume/vol-005433f97d33e3c96"),
      contains(var.source_volume_arns, "arn:aws:ec2:us-west-2:180294223248:volume/vol-0c95953206040fa9c"),
    ])
    error_message = "This trial is restricted to the reviewed 10 GiB root and 20 GiB data volumes."
  }
}

variable "oregon_ebs_key_arn" {
  description = "ARN of the verified us-west-2 aws/ebs key protecting the source volumes."
  type        = string
  default     = "arn:aws:kms:us-west-2:180294223248:key/dd901409-a56e-490c-a106-06fa62d3e4a9"

  validation {
    condition     = var.oregon_ebs_key_arn == "arn:aws:kms:us-west-2:180294223248:key/dd901409-a56e-490c-a106-06fa62d3e4a9"
    error_message = "This trial is restricted to the reviewed Oregon aws/ebs key."
  }
}

variable "oregon_backup_key_arn" {
  description = "Verified Oregon aws/backup KMS key that AWS Backup may need to describe when starting a cross-Region copy."
  type        = string
  default     = "arn:aws:kms:us-west-2:180294223248:key/51696ef6-27c4-402e-922b-712fa1fc0395"

  validation {
    condition     = var.oregon_backup_key_arn == "arn:aws:kms:us-west-2:180294223248:key/51696ef6-27c4-402e-922b-712fa1fc0395"
    error_message = "This trial is restricted to the reviewed Oregon aws/backup key for DescribeKey only."
  }
}

variable "ohio_ebs_key_arn" {
  description = "ARN of the approved us-east-2 aws/ebs KMS key for the destination vault; verify its alias before execution."
  type        = string
  default     = "arn:aws:kms:us-east-2:180294223248:key/3bfad666-3034-4cc5-8ee3-874b954f06fc"

  validation {
    condition     = var.ohio_ebs_key_arn == "arn:aws:kms:us-east-2:180294223248:key/3bfad666-3034-4cc5-8ee3-874b954f06fc"
    error_message = "This trial is restricted to the reviewed Ohio aws/ebs key."
  }
}
