terraform {
  required_version = "~> 1.12.0"

  # The deploy role's own state, beside the infra root's in the same bucket.
  # The deploy role can't read or write this key.
  backend "s3" {
    bucket       = "foundry-tofu-state-793680745829"
    key          = "foundry/bootstrap/terraform.tfstate"
    region       = "us-west-2"
    profile      = "workloads-admin"
    encrypt      = true
    use_lockfile = true
  }

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}
