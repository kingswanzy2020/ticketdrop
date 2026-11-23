terraform {
  required_version = "1.17.2"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
  }

  # This layer creates the bucket, so its first apply ran with local state, which
  # `terraform init -migrate-state` then moved here: no copy lives on a laptop.
  # From a fresh account: comment this block out, apply, uncomment, migrate.
  # Backend blocks cannot read variables, so keep the name equal to
  # var.state_bucket. It has no account ID in it, so the block is committed whole.
  backend "s3" {
    bucket       = "ticketdrop-tfstate-kahmedt"
    key          = "bootstrap/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}
