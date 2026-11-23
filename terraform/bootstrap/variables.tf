variable "state_bucket" {
  description = "The state bucket's name. Global across S3, and without the account ID so the backend blocks can be committed."
  type        = string
  default     = "ticketdrop-tfstate-kahmedt"
}

variable "aws_region" {
  description = "Where the bucket lives. Every layer's backend points at this region."
  type        = string
  default     = "us-east-1"
}

variable "state_version_retention_days" {
  description = "How long superseded state versions are kept."
  type        = number
  default     = 90
}
