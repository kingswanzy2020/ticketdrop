terraform {
  required_version = "1.17.2"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.73.0"
    }
    # The EKS module needs these four; pinned here so the lock file cannot float.
    cloudinit = {
      source  = "hashicorp/cloudinit"
      version = "2.4.1"
    }
    null = {
      source  = "hashicorp/null"
      version = "3.3.2"
    }
    time = {
      source  = "hashicorp/time"
      version = "0.14.2"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "4.4.2"
    }
  }

  backend "s3" {
    bucket       = "ticketdrop-tfstate-kahmedt"
    key          = "cluster/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}
