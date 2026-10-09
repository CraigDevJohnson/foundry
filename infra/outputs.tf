# IDs the runbook steps need (docs/operations.md, infra/README.md). None are
# secret.

output "host_instance_id" {
  description = "The Foundry host instance."
  value       = aws_instance.host.id
}

output "host_public_ip" {
  description = "The Elastic IP for the Cloudflare foundry A record (DNS only)."
  value       = aws_eip.host.public_ip
}

output "root_volume_id" {
  description = "The host's root volume. It changes when the host is rebuilt."
  value       = aws_instance.host.root_block_device[0].volume_id
}

output "data_volume_id" {
  description = "The Foundry data volume (NVMe serial on the host, without the dash)."
  value       = aws_ebs_volume.data.id
}

output "availability_zone" {
  description = "The host's Availability Zone, for restore-test volumes."
  value       = data.aws_subnet.host.availability_zone
}

output "security_group_id" {
  description = "The host security group."
  value       = aws_security_group.foundry.id
}

output "host_control_role_arn" {
  description = "The role the Host control workflow assumes (GitHub variable AWS_HOST_CONTROL_ROLE_ARN)."
  value       = aws_iam_role.host_control.arn
}
