variable "region" {
  description = "AWS region of the target account the ECS clusters and ECR repositories are in."
  type        = string
}

variable "name_prefix" {
  description = "Prefix for the names of the roles. Lowercase letters, digits and hyphens, starting with a letter."
  type        = string
  default     = "yumdrake"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,20}$", var.name_prefix))
    error_message = "name_prefix must be 2 to 21 characters: lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "owner" {
  description = "Who owns these roles, for the owner tag."
  type        = string
}

variable "environment" {
  description = "Short name for the env tag (for example prod, or the name of this target account)."
  type        = string
}

variable "yumdrake_orchestrator_task_role_arn" {
  description = "ARN of Yumdrake's orchestrator task role (output orchestrator_task_role_arn of the root example). Trusted by the discovery role."
  type        = string
}

variable "yumdrake_runner_task_role_arn" {
  description = "ARN of Yumdrake's runner task role (output runner_task_role_arn of the root example). Trusted by the deployment role."
  type        = string
}

variable "discovery_external_id" {
  description = "External id the orchestrator must present to assume the discovery role. Choose a long random value and enter the same one when connecting this account in Yumdrake. It is a shared identifier, not a credential, but keep it out of version control."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.discovery_external_id) >= 16
    error_message = "Use an external id of at least 16 characters."
  }
}

variable "deployment_external_id" {
  description = "External id the runner must present to assume the deployment role. Use a different value from the discovery one."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.deployment_external_id) >= 16
    error_message = "Use an external id of at least 16 characters."
  }
}

variable "ecs_cluster_names" {
  description = "Names of the ECS clusters Yumdrake may read and deploy to in this account."
  type        = list(string)
}

variable "ecr_repository_arns" {
  description = "ARNs of the ECR repositories Yumdrake may read image tags and digests from. A pattern such as arn:aws:ecr:REGION:ACCOUNT:repository/team-* is allowed if you want a whole family; list them explicitly if you can."
  type        = list(string)
}

variable "config_bucket_names" {
  description = "Names of the S3 buckets that hold the configuration files Yumdrake writes. Only these can be written."
  type        = list(string)
}

variable "config_bucket_kms_key_arns" {
  description = "ARNs of the KMS keys that encrypt the config buckets, when they use SSE-KMS. Leave empty when the buckets use S3-managed keys. The deployment role may use these keys only through S3."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for arn in var.config_bucket_kms_key_arns : can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/[A-Za-z0-9-]+$", arn))])
    error_message = "Give full KMS key ARNs (arn:aws:kms:REGION:ACCOUNT:key/ID), with no alias and no wildcard."
  }
}

variable "task_definition_families" {
  description = "Task definition family names of the services Yumdrake deploys. Only revisions of these families can be tagged."
  type        = list(string)
}

variable "passable_role_arns" {
  description = "ARNs of the task roles and task execution roles of the services Yumdrake deploys. These are the only roles the deployment role may pass to ECS. Full ARNs, no wildcard."
  type        = list(string)

  validation {
    condition     = length(var.passable_role_arns) > 0 && alltrue([for arn in var.passable_role_arns : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$", arn))])
    error_message = "Give at least one full IAM role ARN, with no wildcard."
  }
}
