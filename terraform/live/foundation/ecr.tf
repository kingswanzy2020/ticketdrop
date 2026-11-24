# One repository per service. Tags are immutable, so an image pushed as <sha>
# can never be replaced by another under the same tag. Basic scanning on push
# is free. No force_delete: a repository with images in it is not destroyed.
resource "aws_ecr_repository" "service" {
  for_each = var.services

  name                 = "ticketdrop/${each.key}"
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

# Only a count of tagged images for now. A multi-arch push leaves untagged
# per-platform manifests that its index needs, so an untagged-expiry rule waits
# until a real push in Phase 6 shows, in the lifecycle preview, what it removes.
resource "aws_ecr_lifecycle_policy" "service" {
  for_each   = aws_ecr_repository.service
  repository = each.value.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the newest ${var.images_kept} tagged images"
      selection = {
        tagStatus      = "tagged"
        tagPatternList = ["*"]
        countType      = "imageCountMoreThan"
        countNumber    = var.images_kept
      }
      action = { type = "expire" }
    }]
  })
}
