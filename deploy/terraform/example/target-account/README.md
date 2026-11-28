# Roles for an AWS account Yumdrake deploys to

**This is an example to adapt, not a module to apply as it is.** Review every
statement in `iam.tf` and have it reviewed before use. It has been checked with
`terraform fmt` and `terraform validate` only, and never planned or applied.

Apply this once in each AWS account that Yumdrake manages (it may also be the
account Yumdrake itself runs in).

## What it creates

- `<name_prefix>-discovery`: a read-only role. The orchestrator assumes it to
  list ECS clusters, services and tasks and ECR image tags. It trusts only the
  orchestrator task role, and only with the discovery external id.
- `<name_prefix>-deployment`: the role the runner assumes to deploy. It can
  register task definitions, tag revisions of named families, update named
  services, read named ECR repositories, read and write the named config
  buckets, and `iam:PassRole` for the named task and execution roles only, and
  only to ECS tasks. It trusts only the runner task role, and only with the
  deployment external id. It may use the KMS keys in `config_bucket_kms_key_arns`
  through S3 only, and that permission exists only when that variable is set.

## What it assumes already exists

The root example in the directory above, applied once (its outputs give the two
task role ARNs), and in this account the ECS clusters, ECR repositories, config
buckets and task roles you name in the variables.

## What to change first

Copy `terraform.tfvars.example` to `terraform.tfvars` (git-ignored) and replace
every placeholder. Pass the two external ids through the environment, not a file:
`TF_VAR_discovery_external_id` and `TF_VAR_deployment_external_id`, each a long
random value read from your secret store. Use the same values when you connect
the account in Yumdrake. Narrow the lists (clusters, repositories, buckets,
families, roles) to exactly what Yumdrake needs. Set `config_bucket_kms_key_arns`
only when the config buckets use SSE-KMS; with S3-managed keys leave it empty. Compare the ECS and S3 actions
with what the version of Yumdrake you run calls.

## Order of use

1. Apply the root example first, with empty role lists (see its README).
2. Apply this directory in each account, using the root outputs.
3. Return to the root example, add the two role ARNs from this directory's
   outputs to `discovery_role_arns` and `deployment_role_arns`, and apply again.
   The role trust cannot be created before the task roles exist, which is why the
   first step comes first.
