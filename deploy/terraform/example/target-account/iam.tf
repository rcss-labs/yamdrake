# Two roles in the account Yumdrake works on:
#   discovery:  read-only, assumed by the orchestrator, to list clusters, services and image tags.
#   deployment: the changes a deployment makes, assumed by the runner only.
# Each trusts exactly one Yumdrake task role, and only with its external id.

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition

  cluster_arns    = [for name in var.ecs_cluster_names : "arn:${local.partition}:ecs:${var.region}:${local.account_id}:cluster/${name}"]
  service_arns    = [for name in var.ecs_cluster_names : "arn:${local.partition}:ecs:${var.region}:${local.account_id}:service/${name}/*"] # all services in a named cluster
  task_arns       = [for name in var.ecs_cluster_names : "arn:${local.partition}:ecs:${var.region}:${local.account_id}:task/${name}/*"]    # all tasks in a named cluster
  task_definition = [for family in var.task_definition_families : "arn:${local.partition}:ecs:${var.region}:${local.account_id}:task-definition/${family}:*"]
  bucket_arns     = [for name in var.config_bucket_names : "arn:${local.partition}:s3:::${name}"]
  object_arns     = [for name in var.config_bucket_names : "arn:${local.partition}:s3:::${name}/*"] # every object in a named bucket
}

# ---------------------------------------------------------------- discovery

data "aws_iam_policy_document" "discovery_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "AWS"
      identifiers = [var.yumdrake_orchestrator_task_role_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.discovery_external_id]
    }
  }
}

resource "aws_iam_role" "discovery" {
  name               = "${var.name_prefix}-discovery"
  assume_role_policy = data.aws_iam_policy_document.discovery_trust.json
}

data "aws_iam_policy_document" "discovery" {
  # AWS offers no resource-level control for these list and describe calls, so
  # the resource has to be "*". They are read-only.
  statement {
    sid       = "ListClustersAndTaskDefinitions"
    actions   = ["ecs:ListClusters", "ecs:ListTaskDefinitions", "ecs:DescribeTaskDefinition"]
    resources = ["*"]
  }

  statement {
    sid       = "ReadNamedClusters"
    actions   = ["ecs:DescribeClusters", "ecs:ListServices", "ecs:DescribeServices", "ecs:ListTasks", "ecs:DescribeTasks"]
    resources = concat(local.cluster_arns, local.service_arns, local.task_arns)
  }

  statement {
    sid       = "ReadNamedRepositories"
    actions   = ["ecr:DescribeRepositories", "ecr:DescribeImages", "ecr:ListImages"]
    resources = var.ecr_repository_arns
  }
}

resource "aws_iam_role_policy" "discovery" {
  name   = "discovery"
  role   = aws_iam_role.discovery.id
  policy = data.aws_iam_policy_document.discovery.json
}

# ---------------------------------------------------------------- deployment

data "aws_iam_policy_document" "deployment_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "AWS"
      identifiers = [var.yumdrake_runner_task_role_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.deployment_external_id]
    }
  }
}

resource "aws_iam_role" "deployment" {
  name               = "${var.name_prefix}-deployment"
  assume_role_policy = data.aws_iam_policy_document.deployment_trust.json
}

data "aws_iam_policy_document" "deployment" {
  # AWS offers no resource-level control for registering or reading task
  # definitions, so the resource has to be "*".
  statement {
    sid       = "RegisterAndReadTaskDefinitions"
    actions   = ["ecs:RegisterTaskDefinition", "ecs:DescribeTaskDefinition", "ecs:ListTaskDefinitions"]
    resources = ["*"]
  }

  statement {
    sid       = "TagRevisionsOfNamedFamilies"
    actions   = ["ecs:TagResource", "ecs:ListTagsForResource"]
    resources = local.task_definition
  }

  statement {
    sid       = "UpdateAndObserveNamedServices"
    actions   = ["ecs:UpdateService", "ecs:DescribeServices", "ecs:DescribeClusters", "ecs:ListServices", "ecs:ListTasks", "ecs:DescribeTasks"]
    resources = concat(local.cluster_arns, local.service_arns, local.task_arns)
  }

  statement {
    sid       = "ReadNamedRepositories"
    actions   = ["ecr:DescribeRepositories", "ecr:DescribeImages", "ecr:ListImages", "ecr:BatchGetImage"]
    resources = var.ecr_repository_arns
  }

  statement {
    sid       = "ReadAndWriteConfigFiles"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = local.object_arns
  }

  statement {
    sid       = "ListConfigBuckets"
    actions   = ["s3:ListBucket"]
    resources = local.bucket_arns
  }

  statement {
    sid       = "PassOnlyTheNamedRolesToEcs"
    actions   = ["iam:PassRole"]
    resources = var.passable_role_arns
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "deployment" {
  name   = "deployment"
  role   = aws_iam_role.deployment.id
  policy = data.aws_iam_policy_document.deployment.json
}
