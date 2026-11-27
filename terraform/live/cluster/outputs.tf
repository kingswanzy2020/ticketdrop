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

output "cluster_name" {
  description = "The EKS cluster's name."
  value       = module.eks.cluster_name
}

output "cluster_endpoint" {
  description = "The EKS API's address; it answers admin_cidr only."
  value       = module.eks.cluster_endpoint
}

output "kubeconfig_command" {
  description = "Adds the cluster to kubeconfig under a context that cannot be mistaken for docker-desktop."
  value       = "aws eks update-kubeconfig --name ${module.eks.cluster_name} --alias ticketdrop-eks"
}

output "node_security_group_id" {
  description = "The nodes' security group, for rules that let pods reach something outside the cluster."
  value       = module.eks.node_security_group_id
}
