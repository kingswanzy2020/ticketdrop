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

variable "admin_cidr" {
  description = <<-EOT
    The one address the EKS API answers, as a CIDR: your own, from
    `curl -fsS https://checkip.amazonaws.com`, plus /32. No default, so a plan
    without it fails rather than opening the API to everyone.
  EOT
  type        = string

  validation {
    condition     = can(cidrhost(var.admin_cidr, 0)) && !startswith(var.admin_cidr, "0.0.0.0/")
    error_message = "admin_cidr must be a CIDR such as 203.0.113.4/32, and not 0.0.0.0/0."
  }
}

variable "kubernetes_version" {
  description = "EKS Kubernetes version. 1.36 is in standard support until 2 August 2027."
  type        = string
  default     = "1.36"
}

variable "node_instance_type" {
  description = "The node group's instance type: arm64 (Graviton), 2 vCPU, 4 GiB, 17 pods."
  type        = string
  default     = "t4g.medium"
}

variable "network_policy_mode" {
  description = <<-EOT
    How the VPC CNI enforces NetworkPolicy. strict: every pod starts denied,
    and a pod no policy allows stays denied, so everything it must reach,
    CoreDNS included, needs a policy. standard: a new pod is allowed until its
    policies load, about 1.5 seconds on this cluster (TROUBLESHOOTING 18).
  EOT
  type        = string
  default     = "strict"

  validation {
    condition     = contains(["standard", "strict"], var.network_policy_mode)
    error_message = "network_policy_mode must be standard or strict."
  }
}
