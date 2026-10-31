# Network for the workloads host (aws-design §6.2): the default VPC, one subnet,
# and a security group that opens only the player-facing ports. Admin access is
# Session Manager, so there is no SSH or other admin port.

locals {
  # The host, its data volume and every restore-test volume must share one
  # Availability Zone. Pin it by AZ ID, which is the same physical zone in every
  # account; in workloads usw2-az2 is us-west-2a (read-only check, 2026-09-29).
  host_availability_zone_id = "usw2-az2"

  # Player-facing ports, from anywhere on IPv4: 80 for certbot's challenge and
  # the redirect to HTTPS, 443 for Foundry through nginx (ADR 0008). Foundry's
  # own port, 30000, is reached only from the host, so the proxy is the one
  # way in. The default VPC has no IPv6 CIDR, so there are no IPv6 rules.
  public_ingress_ports = {
    http  = 80
    https = 443
  }
}

data "aws_vpc" "default" {
  default = true
}

data "aws_subnet" "host" {
  vpc_id               = data.aws_vpc.default.id
  availability_zone_id = local.host_availability_zone_id
  default_for_az       = true
}

resource "aws_security_group" "foundry" {
  name        = "foundry"
  description = "Foundry VTT host - player ports only, no admin ports"
  vpc_id      = data.aws_vpc.default.id

  tags = {
    Name = "foundry"
  }
}

# Separate rule resources only, never inline blocks.
resource "aws_vpc_security_group_ingress_rule" "public" {
  for_each = local.public_ingress_ports

  security_group_id = aws_security_group.foundry.id
  description       = "${each.key} from anywhere"
  ip_protocol       = "tcp"
  from_port         = each.value
  to_port           = each.value
  cidr_ipv4         = "0.0.0.0/0"
}

# Must stay explicit: when the provider creates a security group it deletes
# AWS's default allow-all egress rule, and the host needs outbound access for
# dnf, the SSM agent, S3 and ACME. The design limits only inbound traffic.
resource "aws_vpc_security_group_egress_rule" "all_ipv4" {
  security_group_id = aws_security_group.foundry.id
  description       = "All outbound IPv4"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}
