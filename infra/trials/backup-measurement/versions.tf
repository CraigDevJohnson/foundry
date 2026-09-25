terraform {
  required_version = ">= 1.12.6, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "= 6.37.0"
    }
  }

  backend "local" {
    path = ""
  }
}

provider "aws" {
  alias               = "oregon"
  region              = "us-west-2"
  allowed_account_ids = [var.account_id]
}

provider "aws" {
  alias               = "ohio"
  region              = "us-east-2"
  allowed_account_ids = [var.account_id]
}
