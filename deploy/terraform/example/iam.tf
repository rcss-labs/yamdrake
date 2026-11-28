# IAM for the two services. Every statement is listed in REVIEW.md.
#
#   orchestrator task role: assumes ONLY discovery roles; decrypts only its own
#     purposes; can encrypt (never decrypt) deployment credentials; reads and
#     writes the log archive. No access to config copies.
#   runner task role: assumes ONLY deployment roles; decrypts deployment
#     credentials and the GitHub connection; uses the config-copies key and bucket.
#   execution role: what ECS itself needs to start the tasks (logs, secrets).

data "aws_iam_policy_document" "tasks_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
  }
}

# ---------------------------------------------------------------- orchestrator

resource "aws_iam_role" "orchestrator_task" {
  name               = local.orchestrator_task_role_name
  assume_role_policy = data.aws_iam_policy_document.tasks_trust.json
}

data "aws_iam_policy_document" "orchestrator_task" {
  # Discovery roles only. With an empty list there is no statement at all.
  dynamic "statement" {
    for_each = length(var.discovery_role_arns) > 0 ? [1] : []
    content {
      sid       = "AssumeDiscoveryRolesOnly"
      actions   = ["sts:AssumeRole"]
      resources = var.discovery_role_arns
    }
  }

  statement {
    sid       = "UseOwnSecretsPurposes"
    actions   = ["kms:Decrypt", "kms:Encrypt"]
    resources = [aws_kms_key.secrets.arn]
    condition {
      test     = "StringEquals"
      variable = "kms:EncryptionContext:purpose"
      values   = local.orchestrator_purposes
    }
  }

  # Encrypt only: the orchestrator stores a deployment credential the user typed
  # but can never read one back.
  statement {
    sid       = "EncryptDeploymentSecretsNeverDecrypt"
    actions   = ["kms:Encrypt"]
    resources = [aws_kms_key.secrets.arn]
    condition {
      test     = "StringEquals"
      variable = "kms:EncryptionContext:purpose"
      values   = [local.deployment_purpose]
    }
  }

  statement {
    sid       = "ReadAndWriteLogArchive"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.log_archive.arn}/*"] # every object in this one bucket: S3 object ARNs end in /*
  }

  statement {
    sid       = "ListLogArchive"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.log_archive.arn]
  }
}

resource "aws_iam_role_policy" "orchestrator_task" {
  name   = "orchestrator"
  role   = aws_iam_role.orchestrator_task.id
  policy = data.aws_iam_policy_document.orchestrator_task.json
}

# ---------------------------------------------------------------- runner

resource "aws_iam_role" "runner_task" {
  name               = local.runner_task_role_name
  assume_role_policy = data.aws_iam_policy_document.tasks_trust.json
}

data "aws_iam_policy_document" "runner_task" {
  # Deployment roles only. With an empty list there is no statement at all.
  dynamic "statement" {
    for_each = length(var.deployment_role_arns) > 0 ? [1] : []
    content {
      sid       = "AssumeDeploymentRolesOnly"
      actions   = ["sts:AssumeRole"]
      resources = var.deployment_role_arns
    }
  }

  statement {
    sid       = "DecryptDeploymentSecretsAndGithub"
    actions   = ["kms:Decrypt"]
    resources = [aws_kms_key.secrets.arn]
    condition {
      test     = "StringEquals"
      variable = "kms:EncryptionContext:purpose"
      values   = local.runner_secret_purposes
    }
  }

  # S3 server-side encryption calls KMS as the caller; ViaService limits the key
  # to requests that come through S3.
  statement {
    sid       = "UseConfigCopiesKeyThroughS3"
    actions   = ["kms:Decrypt", "kms:GenerateDataKey"]
    resources = [aws_kms_key.config_copies.arn]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["s3.${var.region}.amazonaws.com"]
    }
  }

  statement {
    sid       = "ReadAndWriteConfigCopies"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.config_copies.arn}/*"] # every object in this one bucket: S3 object ARNs end in /*
  }

  statement {
    sid       = "ListConfigCopies"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.config_copies.arn]
  }
}

resource "aws_iam_role_policy" "runner_task" {
  name   = "runner"
  role   = aws_iam_role.runner_task.id
  policy = data.aws_iam_policy_document.runner_task.json
}

# ---------------------------------------------------------------- execution role

resource "aws_iam_role" "execution" {
  name               = "${local.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.tasks_trust.json
}

data "aws_iam_policy_document" "execution" {
  statement {
    sid       = "WriteServiceLogs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.orchestrator.arn}:*", "${aws_cloudwatch_log_group.runner.arn}:*"] # :* is the log streams of these two groups
  }

  # The database password is a JSON field of the secret RDS manages. It is
  # encrypted with the AWS-managed Secrets Manager key, whose key policy already
  # lets principals that may call Secrets Manager use it.
  statement {
    sid       = "ReadDatabasePasswordAndRegistryCredentials"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = compact([aws_db_instance.this.master_user_secret[0].secret_arn, var.registry_credentials_secret_arn])
  }
}

resource "aws_iam_role_policy" "execution" {
  name   = "execution"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution.json
}
