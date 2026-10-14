locals {
  workloads_account_id = "793680745829"
  region               = "us-west-2"

  # Made by infra/bootstrap before the Deploy workflow's first run. The
  # controllers and the Host control role carry it, because the deploy role
  # may create or change a role only while it does (ADR 0007).
  workload_boundary_arn = "arn:aws:iam::${local.workloads_account_id}:policy/foundry-workload-boundary"
}

# Default provider: the workloads account, where Foundry and its state live.
provider "aws" {
  region              = local.region
  profile             = var.aws_profile
  allowed_account_ids = [local.workloads_account_id]

  default_tags {
    tags = {
      project = "foundry"
    }
  }

  # The controller writes these on the host when it sends a stop
  # (controller.tf); they are its record, not configuration.
  ignore_tags {
    keys = local.stop_tag_keys
  }
}
