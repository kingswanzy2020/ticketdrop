# The long-lived layer: what stays up between sessions for cents. GitHub's
# OIDC provider and the CI roles, the image registry, the DNS zone and the
# budget. Each piece is used once, so it is written inline, not as modules.

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project   = "ticketdrop"
      Layer     = "foundation"
      ManagedBy = "terraform"
    }
  }
}

data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
}
