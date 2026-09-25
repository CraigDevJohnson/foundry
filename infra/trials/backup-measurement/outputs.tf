output "oregon_vault_arn" {
  description = "Temporary Oregon vault for a separately approved measurement job."
  value       = aws_backup_vault.oregon.arn
}

output "ohio_vault_arn" {
  description = "Temporary Ohio vault for a separately approved copy measurement."
  value       = aws_backup_vault.ohio.arn
}

output "backup_service_role_arn" {
  description = "Foundry-owned role to pass to the approved backup and copy jobs."
  value       = aws_iam_role.backup.arn
}

output "backup_role_boundary_arn" {
  description = "Maximum permissions boundary attached to the passed role."
  value       = aws_iam_policy.backup_boundary.arn
}
