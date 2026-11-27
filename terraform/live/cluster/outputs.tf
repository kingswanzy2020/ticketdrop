output "vpc_id" {
  description = "The cluster's VPC."
  value       = module.vpc.vpc_id
}

output "private_subnets" {
  description = "Private subnet IDs, one per zone, for nodes and internal load balancers."
  value       = module.vpc.private_subnets
}

output "public_subnets" {
  description = "Public subnet IDs, one per zone, for internet-facing load balancers."
  value       = module.vpc.public_subnets
}
