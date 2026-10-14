# Set as the repository variable AWS_DEPLOY_ROLE_ARN (infra/README.md, "The
# deploy role").
output "deployer_role_arn" {
  description = "The role the Deploy workflow assumes."
  value       = aws_iam_role.deployer.arn
}

output "boundary_arn" {
  description = "The permissions boundary the infra root's controllers and Host control role carry."
  value       = aws_iam_policy.boundary.arn
}
