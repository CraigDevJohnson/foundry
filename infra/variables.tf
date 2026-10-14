# Amazon Linux 2023, kernel 6.18, arm64 (al2023-ami-2023.12.20260928.0-kernel-6.18-arm64),
# the same line as the old management host. The instance ignores later
# changes, so a new AMI only takes effect on a deliberate `-replace` of
# aws_instance.host.
variable "ami_id" {
  description = "AMI for the Foundry host (AL2023 arm64). Changes apply only on a deliberate replace of aws_instance.host."
  type        = string
  default     = "ami-0baa3cf0d687ee495"

  validation {
    condition     = can(regex("^ami-[0-9a-f]{8,17}$", var.ami_id))
    error_message = "ami_id must be an AMI ID such as ami-0123456789abcdef0."
  }
}

# Relative to infra/, the working directory of `tofu -chdir=infra`. Run
# `task build` before a plan; a plan without the zip fails.
variable "controller_package_path" {
  description = "The controller's Lambda zip as `task build` writes it: one static arm64 binary named bootstrap."
  type        = string
  default     = "../dist/bootstrap.zip"

  validation {
    condition     = endswith(var.controller_package_path, ".zip")
    error_message = "controller_package_path must name the zip `task build` writes, such as ../dist/bootstrap.zip."
  }
}

# On a computer, Craig's admin profile, pinned so a plan never runs as anything
# else by accident. The Deploy workflow passes deploy.tfvars, which sets it to
# null: OpenTofu then uses the deploy role's credentials from the environment
# (ADR 0007). The backend reads it too, so init and plan take the same value.
variable "aws_profile" {
  description = "The named AWS profile for state and changes, or null for credentials from the environment (the Deploy workflow)."
  type        = string
  default     = "workloads-admin"
}
