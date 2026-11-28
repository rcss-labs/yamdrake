output "orchestrator_task_role_arn" {
  description = "ARN of the orchestrator's task role. Give it to the target-account module as yumdrake_orchestrator_task_role_arn."
  value       = aws_iam_role.orchestrator_task.arn
}

output "runner_task_role_arn" {
  description = "ARN of the runner's task role. Give it to the target-account module as yumdrake_runner_task_role_arn."
  value       = aws_iam_role.runner_task.arn
}

output "load_balancer_dns_name" {
  description = "DNS name of the internal load balancer. Point the orchestrator_hostname record at it."
  value       = aws_lb.orchestrator.dns_name
}

output "orchestrator_url" {
  description = "The address users open (YUMDRAKE_PUBLIC_URL)."
  value       = local.public_url
}

output "ecs_cluster_name" {
  description = "Name of the ECS cluster."
  value       = aws_ecs_cluster.this.name
}

output "database_endpoint" {
  description = "Address of the PostgreSQL database (private)."
  value       = aws_db_instance.this.address
}

output "database_secret_arn" {
  description = "ARN of the Secrets Manager secret RDS keeps the database master password in. Use it for the first-time create-admin step and for restores."
  value       = aws_db_instance.this.master_user_secret[0].secret_arn
}

output "secrets_kms_key_arn" {
  description = "ARN of the KMS key for stored secrets (YUMDRAKE_KMS_KEY_ID)."
  value       = aws_kms_key.secrets.arn
}

output "config_copies_bucket" {
  description = "Name of the bucket that holds config-file copies (YUMDRAKE_ARTIFACT_BUCKET)."
  value       = aws_s3_bucket.config_copies.id
}

output "log_archive_bucket" {
  description = "Name of the bucket that holds archived run logs (YUMDRAKE_LOG_BUCKET)."
  value       = aws_s3_bucket.log_archive.id
}
