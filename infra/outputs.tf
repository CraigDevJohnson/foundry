# IDs the runbook steps need (docs/operations.md, infra/README.md) and the
# controller addresses The Lobby's site takes. None are secret.

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

output "controller_urls" {
  description = "Per Lobby site environment, the controller's Function URL: what the-lobby's foundry_backend_url takes for that environment."
  value       = { for environment, url in aws_lambda_function_url.controller : environment => url.function_url }
}

output "controller_function_arns" {
  description = "Per Lobby site environment, the function behind controller_urls: what the-lobby's foundry_backend_function_arn takes for that environment."
  value       = { for environment, function in aws_lambda_function.controller : environment => function.arn }
}
