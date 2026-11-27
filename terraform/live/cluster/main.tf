# The layer that bills by the hour: the network (step 6) and EKS (step 7). It goes
# up and comes down from a laptop, never from CI, and is destroyed at the end
# of every session.

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project   = "ticketdrop"
      Layer     = "cluster"
      ManagedBy = "terraform"
    }
  }
}

locals {
  # Written out, not looked up, so the zones never shift under a plan. In this
  # account they are use1-az2, use1-az4 and use1-az6. Never us-east-1e
  # (use1-az3): EKS refuses it and it has no Graviton instances.
  azs = ["us-east-1a", "us-east-1b", "us-east-1c"]
}
