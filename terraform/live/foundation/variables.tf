variable "aws_region" {
  description = "Where the registry lives; the same region as the state bucket."
  type        = string
  default     = "us-east-1"
}

variable "github_repo" {
  description = "The repository whose workflows may assume the CI roles, as owner/name."
  type        = string
  default     = "kingswanzy2020/ticketdrop"
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
