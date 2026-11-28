output "discovery_role_arn" {
  description = "ARN of the discovery role. Add it to discovery_role_arns of the root example and enter it when connecting this account in Yumdrake."
  value       = aws_iam_role.discovery.arn
}

output "deployment_role_arn" {
  description = "ARN of the deployment role. Add it to deployment_role_arns of the root example and enter it when connecting this account in Yumdrake."
  value       = aws_iam_role.deployment.arn
}
