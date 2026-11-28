# Security groups. The VPC and subnets already exist (variables).

resource "aws_security_group" "load_balancer" {
  name        = "${local.name}-load-balancer"
  description = "Internal load balancer in front of the orchestrator"
  vpc_id      = var.vpc_id
}

resource "aws_security_group" "orchestrator" {
  name        = "${local.name}-orchestrator"
  description = "Orchestrator tasks"
  vpc_id      = var.vpc_id
}

# The runner has no inbound rule at all: nothing is ever sent to it.
resource "aws_security_group" "runner" {
  name        = "${local.name}-runner"
  description = "Runner tasks (no inbound rules)"
  vpc_id      = var.vpc_id
}

resource "aws_security_group" "database" {
  name        = "${local.name}-database"
  description = "PostgreSQL, reachable from the two services only"
  vpc_id      = var.vpc_id
}

resource "aws_security_group" "cache" {
  name        = "${local.name}-cache"
  description = "Valkey, reachable from the two services only"
  vpc_id      = var.vpc_id
}

# ---- load balancer

resource "aws_vpc_security_group_ingress_rule" "load_balancer_from_clients" {
  for_each = toset(var.client_cidr_blocks)

  security_group_id = aws_security_group.load_balancer.id
  description       = "HTTPS from the client network"
  cidr_ipv4         = each.value
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}

resource "aws_vpc_security_group_egress_rule" "load_balancer_to_orchestrator" {
  security_group_id            = aws_security_group.load_balancer.id
  description                  = "To the orchestrator tasks"
  referenced_security_group_id = aws_security_group.orchestrator.id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
}

# ---- orchestrator

resource "aws_vpc_security_group_ingress_rule" "orchestrator_from_load_balancer" {
  security_group_id            = aws_security_group.orchestrator.id
  description                  = "From the load balancer only"
  referenced_security_group_id = aws_security_group.load_balancer.id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
}

# GitHub, Slack, the identity provider and the AWS APIs are reached over HTTPS.
# 0.0.0.0/0 here is a route, not an exposure: nothing can connect in. Tighten it
# to your NAT or VPC endpoint ranges if you can.
resource "aws_vpc_security_group_egress_rule" "orchestrator_https" {
  security_group_id = aws_security_group.orchestrator.id
  description       = "HTTPS to GitHub, Slack and AWS APIs"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}

resource "aws_vpc_security_group_egress_rule" "orchestrator_to_database" {
  security_group_id            = aws_security_group.orchestrator.id
  description                  = "PostgreSQL"
  referenced_security_group_id = aws_security_group.database.id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_vpc_security_group_egress_rule" "orchestrator_to_cache" {
  security_group_id            = aws_security_group.orchestrator.id
  description                  = "Valkey"
  referenced_security_group_id = aws_security_group.cache.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

# ---- runner (egress only)

resource "aws_vpc_security_group_egress_rule" "runner_https" {
  security_group_id = aws_security_group.runner.id
  description       = "HTTPS to GitHub and AWS APIs (see the note on the orchestrator rule)"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
}

resource "aws_vpc_security_group_egress_rule" "runner_to_database" {
  security_group_id            = aws_security_group.runner.id
  description                  = "PostgreSQL"
  referenced_security_group_id = aws_security_group.database.id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_vpc_security_group_egress_rule" "runner_to_cache" {
  security_group_id            = aws_security_group.runner.id
  description                  = "Valkey"
  referenced_security_group_id = aws_security_group.cache.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

# ---- database and cache (inbound from the two services only)

resource "aws_vpc_security_group_ingress_rule" "database_from_services" {
  for_each = {
    orchestrator = aws_security_group.orchestrator.id
    runner       = aws_security_group.runner.id
  }

  security_group_id            = aws_security_group.database.id
  description                  = "PostgreSQL from the ${each.key}"
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_vpc_security_group_ingress_rule" "cache_from_services" {
  for_each = {
    orchestrator = aws_security_group.orchestrator.id
    runner       = aws_security_group.runner.id
  }

  security_group_id            = aws_security_group.cache.id
  description                  = "Valkey from the ${each.key}"
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}
