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
