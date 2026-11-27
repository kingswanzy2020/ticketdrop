variable "aws_region" {
  description = "Region for every resource in this layer."
  type        = string
  default     = "us-east-1"
}

variable "cluster_name" {
  description = "The EKS cluster's name; Karpenter finds the private subnets by it."
  type        = string
  default     = "ticketdrop"
}

variable "vpc_cidr" {
  description = "The VPC's address range. Must not overlap the default VPC (172.31.0.0/16)."
  type        = string
  default     = "10.40.0.0/16"
}
