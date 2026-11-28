data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  name       = "${var.name_prefix}-${var.environment}"
  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition

  tags = merge(var.extra_tags, {
    app   = "yumdrake"
    env   = var.environment
    owner = var.owner
  })

  orchestrator_image = "${var.image_repository}/yumdrake-orchestrator:${var.image_version}"
  runner_image       = "${var.image_repository}/yumdrake-runner:${var.image_version}"

  # The task roles are named here, not read from the role resources, so the KMS
  # key policies can name them without a dependency cycle (the roles' policies
  # name the keys).
  orchestrator_task_role_name = "${local.name}-orchestrator-task"
  runner_task_role_name       = "${local.name}-runner-task"
  orchestrator_task_role_arn  = "arn:${local.partition}:iam::${local.account_id}:role/${local.orchestrator_task_role_name}"
  runner_task_role_arn        = "arn:${local.partition}:iam::${local.account_id}:role/${local.runner_task_role_name}"

  # Encryption-context purposes (the application passes {"purpose": <name>}).
  # Split by who may decrypt: see kms.tf and iam.tf.
  orchestrator_purposes = ["aws-discovery", "github-connection", "slack", "saml-sp-key", "saml-relay-key"]
  runner_secret_purposes = [
    "aws-deploy",        # deployment credentials: the runner only
    "github-connection", # the runner reads env files from GitHub too
  ]
  deployment_purpose = "aws-deploy"

  config_copies_bucket_name = "${local.name}-config-copies-${local.account_id}"
  log_archive_bucket_name   = "${local.name}-log-archive-${local.account_id}"

  database_url = "postgres://${var.database_username}@${aws_db_instance.this.address}:${aws_db_instance.this.port}/${var.database_name}?sslmode=require"
  valkey_addr  = "${aws_elasticache_replication_group.this.primary_endpoint_address}:6379"
  public_url   = "https://${var.orchestrator_hostname}"
}
