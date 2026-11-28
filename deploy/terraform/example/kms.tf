# Two KMS keys.
#
# secrets: the key the application is given (YUMDRAKE_KMS_KEY_ID) for the
#   secrets it stores (AWS, GitHub and Slack credentials, the SAML key). The
#   application encrypts each secret with the encryption context
#   {"purpose": <name>}. Who may decrypt what is decided by that purpose:
#   - the orchestrator decrypts the discovery, Slack and SAML secrets and the
#     GitHub connection; it can only ENCRYPT deployment credentials, never read them
#   - the runner decrypts deployment credentials and the GitHub connection
#   The same rule is written twice: in the task roles (iam.tf) and as a Deny in
#   the key policy below, so a mistake in one place does not open the other.
#
# config-copies: encrypts the bucket of config-file copies. Only the runner can
#   use it; the orchestrator never reads config contents.

locals {
  key_administrator_actions = [
    "kms:CancelKeyDeletion",
    "kms:DescribeKey",
    "kms:DisableKey",
    "kms:DisableKeyRotation",
    "kms:EnableKey",
    "kms:EnableKeyRotation",
    "kms:GetKeyPolicy",
    "kms:GetKeyRotationStatus",
    "kms:ListGrants",
    "kms:ListKeyPolicies",
    "kms:ListResourceTags",
    "kms:PutKeyPolicy",
    "kms:ScheduleKeyDeletion",
    "kms:TagResource",
    "kms:UntagResource",
    "kms:UpdateKeyDescription",
  ]
}

data "aws_iam_policy_document" "secrets_key" {
  # AWS requires the account root in a key policy, otherwise IAM policies cannot
  # grant access to the key and the key can become unmanageable. kms:* and the
  # resource "*" (meaning this key) are how AWS writes that default statement.
  # The Deny statement below still applies to everyone, root included.
  statement {
    sid       = "EnableIamPolicies"
    actions   = ["kms:*"]
    resources = ["*"] # in a key policy, "*" means this key only
    principals {
      type        = "AWS"
      identifiers = ["arn:${local.partition}:iam::${local.account_id}:root"]
    }
  }

  statement {
    sid       = "KeyAdministrationWithoutUse"
    actions   = local.key_administrator_actions
    resources = ["*"] # this key only
    principals {
      type        = "AWS"
      identifiers = var.kms_key_administrator_arns
    }
  }

  # Only the runner's task role may decrypt deployment credentials. The principal
  # is "*" because a Deny must catch every caller; the condition exempts the runner.
  statement {
    sid       = "OnlyTheRunnerDecryptsDeploymentSecrets"
    effect    = "Deny"
    actions   = ["kms:Decrypt", "kms:ReEncryptFrom"]
    resources = ["*"] # this key only
    principals {
      type        = "AWS"
      identifiers = ["*"]
    }
    condition {
      test     = "StringEquals"
      variable = "kms:EncryptionContext:purpose"
      values   = [local.deployment_purpose]
    }
    condition {
      test     = "ArnNotEquals"
      variable = "aws:PrincipalArn"
      values   = [local.runner_task_role_arn]
    }
  }
}

resource "aws_kms_key" "secrets" {
  description             = "Yumdrake: stored secrets, separated by encryption context purpose"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.secrets_key.json
}

resource "aws_kms_alias" "secrets" {
  name          = "alias/${local.name}-secrets"
  target_key_id = aws_kms_key.secrets.key_id
}

data "aws_iam_policy_document" "config_copies_key" {
  # See the note on the same statement in secrets_key.
  statement {
    sid       = "EnableIamPolicies"
    actions   = ["kms:*"]
    resources = ["*"] # this key only
    principals {
      type        = "AWS"
      identifiers = ["arn:${local.partition}:iam::${local.account_id}:root"]
    }
  }

  statement {
    sid       = "KeyAdministrationWithoutUse"
    actions   = local.key_administrator_actions
    resources = ["*"] # this key only
    principals {
      type        = "AWS"
      identifiers = var.kms_key_administrator_arns
    }
  }

  # Nobody but the runner's task role may read or write data under this key.
  statement {
    sid       = "OnlyTheRunnerUsesTheKey"
    effect    = "Deny"
    actions   = ["kms:Decrypt", "kms:ReEncryptFrom", "kms:GenerateDataKey", "kms:GenerateDataKeyWithoutPlaintext"]
    resources = ["*"] # this key only
    principals {
      type        = "AWS"
      identifiers = ["*"] # a Deny must catch every caller
    }
    condition {
      test     = "ArnNotEquals"
      variable = "aws:PrincipalArn"
      values   = [local.runner_task_role_arn]
    }
  }
}

resource "aws_kms_key" "config_copies" {
  description             = "Yumdrake: copies of configuration files, usable by the runner only"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.config_copies_key.json
}

resource "aws_kms_alias" "config_copies" {
  name          = "alias/${local.name}-config-copies"
  target_key_id = aws_kms_key.config_copies.key_id
}
