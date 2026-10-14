locals {
  workloads_account_id = "793680745829"
  region               = "us-west-2"
}

# Always Craig's admin profile: this root makes the role the Deploy workflow
# signs in as, so the workflow can never apply it (infra/README.md, "The
# deploy role").
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
