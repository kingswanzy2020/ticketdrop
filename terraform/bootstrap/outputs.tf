output "state_bucket" {
  description = "The state bucket every layer's backend block names."
  value       = aws_s3_bucket.state.id
}

output "state_bucket_arn" {
  description = "For the CI roles' policies in live/foundation."
  value       = aws_s3_bucket.state.arn
}
