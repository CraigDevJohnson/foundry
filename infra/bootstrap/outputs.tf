# Set as the repository variable AWS_DEPLOY_ROLE_ARN (infra/README.md, "The
# deploy role").
output "deployer_role_arn" {
  description = "The role the Deploy workflow assumes."
  value       = aws_iam_role.deployer.arn
}

output "boundary_arn" {
  description = "The permissions boundary the controllers and the Host control role carry."
  value       = aws_iam_policy.boundary.arn
}

# Set as the repository variable AWS_HOST_CONTROL_ROLE_ARN (docs/operations.md).
output "host_control_role_arn" {
  description = "The role the Host control workflow assumes."
  value       = aws_iam_role.host_control.arn
}
