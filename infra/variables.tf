# Pending decision D7 (refactor plan §4). The recommended answer is to remove
# the CloudWatch agent in Phase 3, so the policy is detached by default. Set to
# true only if Craig defers D7; Phase 3b then sets it back to false.
variable "keep_cloudwatch_agent" {
  description = "Keep CloudWatchAgentServerPolicy on the interim foundry host role (refactor decision D7)."
  type        = bool
  default     = false
}
