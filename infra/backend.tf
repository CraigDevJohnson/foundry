# State lives in the workloads account from the start, so it never moves when
# Foundry does (aws-design §13, fact 2). The bucket is created by CLI in
# refactor Phase 3 and then imported in state.tf.
terraform {
  backend "s3" {
    bucket       = "foundry-tofu-state-793680745829"
    key          = "foundry/terraform.tfstate"
    region       = "us-west-2"
    profile      = "workloads-admin"
    encrypt      = true
    use_lockfile = true
  }
}
