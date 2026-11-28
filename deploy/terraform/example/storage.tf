# Two private, encrypted, versioned buckets.
#   config copies: encrypted with the config-copies key, objects expire after 30 days.
#   log archive:   encrypted with S3-managed keys (the orchestrator writes and
#                  reads it), objects expire after var.log_archive_retention_days.

resource "aws_s3_bucket" "config_copies" {
  bucket = local.config_copies_bucket_name
}

resource "aws_s3_bucket" "log_archive" {
  bucket = local.log_archive_bucket_name
}

locals {
  buckets = {
    config_copies = aws_s3_bucket.config_copies
    log_archive   = aws_s3_bucket.log_archive
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  for_each = local.buckets

  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "this" {
  for_each = local.buckets

  bucket = each.value.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "this" {
  for_each = local.buckets

  bucket = each.value.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "config_copies" {
  bucket = aws_s3_bucket.config_copies.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.config_copies.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "log_archive" {
  bucket = aws_s3_bucket.log_archive.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Config copies are deleted 30 days after they are written. Older versions go
# the same way, so a deleted copy does not linger.
resource "aws_s3_bucket_lifecycle_configuration" "config_copies" {
  bucket = aws_s3_bucket.config_copies.id

  rule {
    id     = "expire-after-30-days"
    status = "Enabled"
    filter {}

    expiration {
      days = 30
    }
    noncurrent_version_expiration {
      noncurrent_days = 1
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

resource "aws_s3_bucket_lifecycle_configuration" "log_archive" {
  bucket = aws_s3_bucket.log_archive.id

  rule {
    id     = "expire-after-retention"
    status = "Enabled"
    filter {}

    expiration {
      days = var.log_archive_retention_days
    }
    noncurrent_version_expiration {
      noncurrent_days = 30
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

data "aws_iam_policy_document" "bucket_tls_only" {
  for_each = local.buckets

  # A Deny for everyone who does not use TLS. The principal is "*" because the
  # statement must cover every caller.
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"] # every S3 action: a Deny that must not leave a plain-HTTP path
    resources = [each.value.arn, "${each.value.arn}/*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "tls_only" {
  for_each = local.buckets

  bucket = each.value.id
  policy = data.aws_iam_policy_document.bucket_tls_only[each.key].json

  depends_on = [aws_s3_bucket_public_access_block.this]
}
