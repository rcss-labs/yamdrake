# Alarms on the metrics the orchestrator emits (namespace Yumdrake). All go to
# the SNS topic given in var.alarm_topic_arn. Missing data means the orchestrator
# is not emitting, which is itself a problem, so it breaches.

locals {
  alarms = {
    oldest-queued-leg-age = {
      metric      = "OldestQueuedLegAge"
      statistic   = "Maximum"
      operator    = "GreaterThanThreshold"
      threshold   = var.oldest_queued_leg_age_alarm_seconds
      description = "A leg has waited in the queue longer than the limit: runners are missing, stuck or too few."
    }
    active-runners = {
      metric      = "ActiveRunners"
      statistic   = "Minimum"
      operator    = "LessThanThreshold"
      threshold   = 1
      description = "No runner is active, so nothing can be deployed."
    }
    log-persistence-failures = {
      metric      = "LogPersistenceFailures"
      statistic   = "Sum"
      operator    = "GreaterThanThreshold"
      threshold   = 0
      description = "Run logs could not be stored."
    }
    aws-throttles = {
      metric      = "AWSThrottles"
      statistic   = "Sum"
      operator    = "GreaterThanThreshold"
      threshold   = var.aws_throttles_alarm_threshold
      description = "AWS is throttling Yumdrake's calls (resolve, revalidate, config copies, deployments, versions): too many parallel runs or too small a quota."
    }
    notification-failures = {
      metric      = "NotificationFailures"
      statistic   = "Sum"
      operator    = "GreaterThanThreshold"
      threshold   = 0
      description = "Notifications (for example to Slack) could not be sent."
    }
  }
}

resource "aws_cloudwatch_metric_alarm" "this" {
  for_each = local.alarms

  alarm_name          = "${local.name}-${each.key}"
  alarm_description   = each.value.description
  namespace           = "Yumdrake"
  metric_name         = each.value.metric
  dimensions          = var.metric_dimensions
  statistic           = each.value.statistic
  comparison_operator = each.value.operator
  threshold           = each.value.threshold
  period              = 60
  evaluation_periods  = 5
  datapoints_to_alarm = 3
  treat_missing_data  = "breaching"

  alarm_actions = [var.alarm_topic_arn]
  ok_actions    = [var.alarm_topic_arn]
}
