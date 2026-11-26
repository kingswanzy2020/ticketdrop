variable "aws_region" {
  description = "Where the registry lives; the same region as the state bucket."
  type        = string
  default     = "us-east-1"
}

variable "github_sub_prefix" {
  description = <<-EOT
    The start of the `sub` claim in this repository's OIDC tokens. GitHub issues
    immutable subjects, which carry the owner and repository IDs, so a renamed or
    re-created repository cannot inherit the trust. Read it with
    `gh api repos/OWNER/REPO/actions/oidc/customization/sub`.
  EOT
  type        = string
  default     = "repo:kingswanzy2020@84867045/ticketdrop@1402918619"
}

variable "services" {
  description = "One ECR repository each, named ticketdrop/<service>, as the CI build job tags them."
  type        = set(string)
  default = [
    "catalog", "fulfillment", "gateway", "inventory", "notifications",
    "orders", "payments", "scheduler", "web",
  ]
}

variable "images_kept" {
  description = "How many tagged images each repository keeps; older ones expire."
  type        = number
  default     = 30
}

variable "budget_email" {
  description = "Where budget alerts go. Set in terraform.tfvars, which is gitignored."
  type        = string
  sensitive   = true
}
