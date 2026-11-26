# The S3 bucket that holds the Terraform state of every layer, each under its
# own key: bootstrap/, foundation/, cluster/. Run once per account.
#
# Locking is use_lockfile: each run writes <key>.tflock with a conditional
# write, so there is no DynamoDB table.

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project   = "ticketdrop"
      Layer     = "bootstrap"
      ManagedBy = "terraform"
    }
  }
}

resource "aws_s3_bucket" "state" {
  bucket = var.state_bucket

  # Losing this bucket loses track of everything every layer created. And a
  # deleted bucket's name is free for anyone to take, along with whatever state
  # an old backend block would then write to it.
  lifecycle {
    prevent_destroy = true
  }
}

# The way back from a corrupted or truncated state file.
resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id

  versioning_configuration {
    status = "Enabled"
  }
}

# State holds every attribute in plain text. SSE-S3: a KMS key would add a
# monthly charge and nothing else here (DECISIONS 68), so Trivy's CMK rule is
# accepted rather than met.
#trivy:ignore:AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket = aws_s3_bucket.state.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Every run creates and deletes a .tflock object, and on a versioned bucket each
# delete leaves a delete marker; this clears those along with old versions.
resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    id     = "expire-old-state-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = var.state_version_retention_days
    }

    expiration {
      expired_object_delete_marker = true
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.state]
}

# Refuses any request that is not over TLS.
data "aws_iam_policy_document" "deny_insecure_transport" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    resources = [
      aws_s3_bucket.state.arn,
      "${aws_s3_bucket.state.arn}/*",
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = data.aws_iam_policy_document.deny_insecure_transport.json

  depends_on = [aws_s3_bucket_public_access_block.state]
}
