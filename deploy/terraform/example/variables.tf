# Every site-specific value is a variable. Required variables have no default.
# See terraform.tfvars.example for placeholders.

# ---------------------------------------------------------------- where

variable "region" {
  description = "AWS region to create everything in."
  type        = string
}

variable "name_prefix" {
  description = "Prefix for the names of all resources. Lowercase letters, digits and hyphens, starting with a letter."
  type        = string
  default     = "yumdrake"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,20}$", var.name_prefix))
    error_message = "name_prefix must be 2 to 21 characters: lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "environment" {
  description = "Short name of this installation (for example prod or staging). Used in the env tag and in resource names."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,15}$", var.environment))
    error_message = "environment must be 2 to 16 characters: lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "owner" {
  description = "Who owns this installation, for the owner tag (a team name or a mailing list)."
  type        = string
}

variable "extra_tags" {
  description = "Additional tags for every resource."
  type        = map(string)
  default     = {}
}

# ---------------------------------------------------------------- network (all existing)

variable "vpc_id" {
  description = "ID of an existing VPC to put everything in."
  type        = string
}

variable "private_subnet_ids" {
  description = "IDs of at least two existing private subnets in different availability zones. Used for the load balancer, the tasks, the database and the cache."
  type        = list(string)

  validation {
    condition     = length(var.private_subnet_ids) >= 2
    error_message = "Give at least two private subnets in different availability zones."
  }
}

variable "client_cidr_blocks" {
  description = "CIDR blocks of the networks that may reach the internal load balancer on port 443 (for example the office or VPN range). Never 0.0.0.0/0."
  type        = list(string)

  validation {
    condition     = length(var.client_cidr_blocks) > 0 && !contains(var.client_cidr_blocks, "0.0.0.0/0")
    error_message = "Give at least one CIDR block, and not 0.0.0.0/0."
  }
}

# ---------------------------------------------------------------- front door

variable "orchestrator_hostname" {
  description = "Host name users type to reach Yumdrake (for example yumdrake.internal.example.com). You create the DNS record yourself, pointing at the load_balancer_dns_name output. It becomes YUMDRAKE_PUBLIC_URL."
  type        = string
}

variable "certificate_arn" {
  description = "ARN of an existing ACM certificate in this region that covers orchestrator_hostname."
  type        = string
}

variable "load_balancer_idle_timeout_seconds" {
  description = "Idle timeout of the load balancer. It must stay well above the interval at which the orchestrator sends Server-Sent Events heartbeats, or the live event stream is cut."
  type        = number
  default     = 300
}

# ---------------------------------------------------------------- images and sizing

variable "image_repository" {
  description = "Registry path that holds the two images, without a tag (for example ghcr.io/your-org). Images are <image_repository>/yumdrake-orchestrator:<image_version> and yumdrake-runner."
  type        = string
}

variable "image_version" {
  description = "Exact version tag of both images (for example v1.2.3). Never latest."
  type        = string

  validation {
    condition     = var.image_version != "latest" && var.image_version != ""
    error_message = "Use an exact version tag, not latest."
  }
}

variable "registry_credentials_secret_arn" {
  description = "ARN of a Secrets Manager secret holding {username, password} for a private registry. Leave null if the images can be pulled anonymously or the task execution role can pull them by other means."
  type        = string
  default     = null
}

variable "cpu_architecture" {
  description = "CPU architecture of the Fargate tasks. Both images are published for both."
  type        = string
  default     = "X86_64"

  validation {
    condition     = contains(["X86_64", "ARM64"], var.cpu_architecture)
    error_message = "cpu_architecture must be X86_64 or ARM64."
  }
}

variable "orchestrator_desired_count" {
  description = "Number of orchestrator tasks. Two or more for availability."
  type        = number
  default     = 2
}

variable "orchestrator_cpu" {
  description = "Fargate CPU units for each orchestrator task."
  type        = number
  default     = 512
}

variable "orchestrator_memory" {
  description = "Fargate memory in MiB for each orchestrator task."
  type        = number
  default     = 1024
}

variable "runner_min_count" {
  description = "Fewest runner tasks. Keep at 2 or more so one runner can resume the work of another."
  type        = number
  default     = 2
}

variable "runner_max_count" {
  description = "Most runner tasks the queue-depth scaling may start."
  type        = number
  default     = 10
}

variable "runner_cpu" {
  description = "Fargate CPU units for each runner task."
  type        = number
  default     = 512
}

variable "runner_memory" {
  description = "Fargate memory in MiB for each runner task."
  type        = number
  default     = 1024
}

variable "runner_concurrency" {
  description = "Legs one runner executes at once (YUMDRAKE_RUNNER_CONCURRENCY)."
  type        = number
  default     = 8
}

variable "runner_queue_depth_target" {
  description = "Target value of the QueueDepth metric for runner scaling. Above it, more runners start."
  type        = number
  default     = 10
}

variable "metric_dimensions" {
  description = "Dimensions the application puts on its CloudWatch metrics, as name to value. Empty if it sets none. Must match what the orchestrator emits for the alarms and scaling to see data."
  type        = map(string)
  default     = {}
}

variable "log_level" {
  description = "Application log level (YUMDRAKE_LOG_LEVEL): debug, info, warn or error."
  type        = string
  default     = "info"
}

# ---------------------------------------------------------------- application settings read from later plans

variable "aws_endpoint_url" {
  description = "Override of the AWS API endpoint (YUMDRAKE_AWS_ENDPOINT_URL). Leave empty on real AWS; it exists for local test setups."
  type        = string
  default     = ""
}

variable "metrics_mode" {
  description = "Value of YUMDRAKE_METRICS. emf makes the orchestrator write CloudWatch embedded-metric-format log lines every 60 seconds; the alarms and scaling here depend on it."
  type        = string
  default     = "emf"
}

# ---------------------------------------------------------------- connected accounts

variable "discovery_role_arns" {
  description = "ARNs of the discovery roles (read-only) in the AWS accounts Yumdrake manages. The orchestrator may assume exactly these and nothing else. May be empty at first, and filled in once target-account is applied."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for arn in var.discovery_role_arns : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$", arn))])
    error_message = "Each entry must be a full IAM role ARN with no wildcard."
  }
}

variable "deployment_role_arns" {
  description = "ARNs of the deployment roles in the AWS accounts Yumdrake deploys to. The runner may assume exactly these and nothing else. May be empty at first."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for arn in var.deployment_role_arns : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$", arn))])
    error_message = "Each entry must be a full IAM role ARN with no wildcard."
  }
}

# ---------------------------------------------------------------- keys and storage

variable "kms_key_administrator_arns" {
  description = "ARNs of the IAM roles or users that administer the two KMS keys (not use them). Not the task roles."
  type        = list(string)

  validation {
    condition     = length(var.kms_key_administrator_arns) > 0
    error_message = "Name at least one key administrator, or nobody can manage the keys."
  }
}

variable "log_archive_retention_days" {
  description = "Days an archived run log is kept in the log bucket before it expires. Also sets YUMDRAKE_LOG_ARCHIVE_RETENTION so the orchestrator drops the index rows with the objects. The default matches the orchestrator's own default of 8760h."
  type        = number
  default     = 365
}

variable "cloudwatch_log_retention_days" {
  description = "Days the service logs are kept in CloudWatch Logs. Must be a value CloudWatch accepts (for example 30, 90, 365)."
  type        = number
  default     = 90
}

# ---------------------------------------------------------------- database

variable "database_name" {
  description = "Name of the PostgreSQL database."
  type        = string
  default     = "yumdrake"
}

variable "database_username" {
  description = "Master user name of the database. Its password is generated and kept in Secrets Manager by RDS; it is never in Terraform configuration."
  type        = string
  default     = "yumdrake"
}

variable "database_engine_version" {
  description = "PostgreSQL version. A major version alone (for example 17) lets RDS pick the minor."
  type        = string
  default     = "17"
}

variable "database_parameter_group_family" {
  description = "Parameter group family matching database_engine_version (for example postgres17)."
  type        = string
  default     = "postgres17"
}

variable "database_instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.t4g.medium"
}

variable "database_allocated_storage_gb" {
  description = "Initial database storage in GiB."
  type        = number
  default     = 50
}

variable "database_max_allocated_storage_gb" {
  description = "Upper limit in GiB for storage autoscaling."
  type        = number
  default     = 200
}

variable "database_backup_retention_days" {
  description = "Days of automated backups, and so of point-in-time recovery. At least 1."
  type        = number
  default     = 14

  validation {
    condition     = var.database_backup_retention_days >= 1 && var.database_backup_retention_days <= 35
    error_message = "Backups (and point-in-time recovery) need 1 to 35 days."
  }
}

variable "database_multi_az" {
  description = "Keep a standby database in a second availability zone."
  type        = bool
  default     = true
}

# ---------------------------------------------------------------- cache

variable "cache_node_type" {
  description = "ElastiCache node type."
  type        = string
  default     = "cache.t4g.small"
}

variable "cache_engine_version" {
  description = "Valkey version."
  type        = string
  default     = "8.0"
}

variable "cache_parameter_group_name" {
  description = "Parameter group matching cache_engine_version (for example default.valkey8)."
  type        = string
  default     = "default.valkey8"
}

variable "cache_node_count" {
  description = "Number of cache nodes (one primary plus replicas). Two or more allows automatic failover."
  type        = number
  default     = 2
}

# ---------------------------------------------------------------- alarms

variable "alarm_topic_arn" {
  description = "ARN of an existing SNS topic that receives the alarms."
  type        = string
}

variable "aws_throttles_alarm_threshold" {
  description = "Alarm when AWS throttles Yumdrake more than this many times in a minute (the alarm needs 3 of 5 such minutes)."
  type        = number
  default     = 10
}

variable "oldest_queued_leg_age_alarm_seconds" {
  description = "Alarm when the oldest queued leg has waited longer than this many seconds."
  type        = number
  default     = 300
}
