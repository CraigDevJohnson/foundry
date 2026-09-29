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

# TEMPORARY: no resource uses this provider any more. It stays only for the
# refactor Phase 9 apply, which destroys the interim management resources
# that were in mgmt_interim.tf and migration.tf; state still records them
# under this provider, so the destroy needs it. A follow-up commit removes this
# block once that apply has succeeded and a plan is clean. Never switch it to
# workloads in place (aws-design §13, fact 2).
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
