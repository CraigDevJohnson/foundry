locals {
  workloads_account_id  = "793680745829"
  management_account_id = "180294223248"
  region                = "us-west-2"
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

# Interim provider for the hand-built host that still runs in management.
# Only mgmt_interim.tf uses it. When Foundry moves, the workloads copies are
# added on the default provider and these resources are removed from config;
# never switch this provider to workloads in place (aws-design §13, fact 2).
provider "aws" {
  alias               = "mgmt"
  region              = local.region
  profile             = "mgmt-admin"
  allowed_account_ids = [local.management_account_id]

  default_tags {
    tags = {
      project = "foundry"
    }
  }
}
