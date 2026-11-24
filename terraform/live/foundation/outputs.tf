output "ci_role_arns" {
  description = "For aws-actions/configure-aws-credentials in each workflow job."
  value       = { for k, r in aws_iam_role.ci : k => r.arn }
}

output "ecr_repository_urls" {
  description = "Where CI pushes each service's image."
  value       = { for k, r in aws_ecr_repository.service : k => r.repository_url }
}

output "zone_id" {
  value = aws_route53_zone.main.zone_id
}

output "name_servers" {
  description = "What Porkbun must list for kahmedt.com."
  value       = aws_route53_zone.main.name_servers
}
