# Internal HTTPS load balancer in front of the orchestrator. There is no
# listener on port 80 and no internet-facing address.

resource "aws_lb" "orchestrator" {
  name               = "${local.name}-orchestrator"
  internal           = true
  load_balancer_type = "application"
  security_groups    = [aws_security_group.load_balancer.id]
  subnets            = var.private_subnet_ids

  # The live event stream (Server-Sent Events) keeps one connection open for
  # minutes without traffic between events. The default idle timeout of 60
  # seconds would cut it, so it is raised (300 seconds by default). The load
  # balancer does not buffer responses, so events pass through as they come.
  idle_timeout = var.load_balancer_idle_timeout_seconds

  drop_invalid_header_fields = true
  enable_deletion_protection = true
}

resource "aws_lb_target_group" "orchestrator" {
  name        = "${local.name}-orchestrator"
  vpc_id      = var.vpc_id
  port        = 8080
  protocol    = "HTTP"
  target_type = "ip" # required for Fargate (awsvpc) tasks

  # Let long-lived event streams finish or reconnect during a deployment.
  deregistration_delay = 60

  health_check {
    path                = "/readyz"
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.orchestrator.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.orchestrator.arn
  }
}
