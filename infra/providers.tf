locals {
  workloads_account_id = "793680745829"
  region               = "us-west-2"
}

# Default provider: the workloads account, where Foundry and its state live.
provider "aws" {
  region              = local.region
  profile             = "workloads-admin"
  allowed_account_ids = [local.workloads_account_id]

  default_tags {
    tags = {
      project = "foundry"
    }
  }
}
