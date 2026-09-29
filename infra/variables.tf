# Pending decision D7 (refactor plan §4). The recommended answer is to remove
# the CloudWatch agent in Phase 3, so the policy is detached by default. If
# Craig defers D7, flip it by committing default = true here; Phase 3b commits
# it back to false. Never flip it with a one-off -var: every later plain plan
# would then propose detaching the policy again.
variable "keep_cloudwatch_agent" {
  description = "Keep CloudWatchAgentServerPolicy on the interim foundry host role (refactor decision D7). Change it only by committing a new default, never with -var."
  type        = bool
  default     = false
}

# Amazon Linux 2023, kernel 6.18, arm64 (al2023-ami-2023.12.20260928.0-kernel-6.18-arm64),
# the same line as the management host. The instance ignores later changes, so
# a new AMI only takes effect on a deliberate `-replace` of aws_instance.host.
variable "ami_id" {
  description = "AMI for the Foundry host (AL2023 arm64). Changes apply only on a deliberate replace of aws_instance.host."
  type        = string
  default     = "ami-0baa3cf0d687ee495"

  validation {
    condition     = can(regex("^ami-[0-9a-f]{8,17}$", var.ami_id))
    error_message = "ami_id must be an AMI ID such as ami-0123456789abcdef0."
  }
}
