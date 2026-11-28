# Hand review of the Terraform example

Done by reading the code against the Global Constraints of plan 11, after
`make tf-check` passed. Not a substitute for your own review: the example is
never planned or applied here.

## 1. Every IAM and policy statement

`*` column: what wildcard the statement contains and why it is needed. "none" means
no action, resource or principal wildcard.

### Root example, identity policies (`iam.tf`)

| # | Role | Sid | Actions | Resources | `*` |
|---|---|---|---|---|---|
| 1 | all three roles | trust (`tasks_trust`) | `sts:AssumeRole` | principal `ecs-tasks.amazonaws.com`, `aws:SourceAccount` equals this account | none |
| 2 | orchestrator task | AssumeDiscoveryRolesOnly | `sts:AssumeRole` | `var.discovery_role_arns` (validated: full ARNs, no wildcard; statement absent when empty) | none |
| 3 | orchestrator task | UseOwnSecretsPurposes | `kms:Decrypt`, `kms:Encrypt` | secrets key; condition `purpose` in `aws-discovery`, `github-connection`, `slack`, `saml-sp-key` | none |
| 4 | orchestrator task | EncryptDeploymentSecretsNeverDecrypt | `kms:Encrypt` | secrets key; condition `purpose` = `aws-deploy` | none |
| 5 | orchestrator task | ReadAndWriteLogArchive | `s3:GetObject`, `s3:PutObject` | `<log bucket>/*` | `/*`: S3 object ARNs end in `/*`; it is every object of this one bucket |
| 6 | orchestrator task | ListLogArchive | `s3:ListBucket` | log bucket | none |
| 7 | runner task | AssumeDeploymentRolesOnly | `sts:AssumeRole` | `var.deployment_role_arns` (validated; absent when empty) | none |
| 8 | runner task | DecryptDeploymentSecretsAndGithub | `kms:Decrypt` | secrets key; condition `purpose` in `aws-deploy`, `github-connection` | none |
| 9 | runner task | UseConfigCopiesKeyThroughS3 | `kms:Decrypt`, `kms:GenerateDataKey` | config-copies key; condition `kms:ViaService` = S3 in this region | none |
| 10 | runner task | ReadAndWriteConfigCopies | `s3:GetObject`, `s3:PutObject` | `<config bucket>/*` | `/*`: as 5 |
| 11 | runner task | ListConfigCopies | `s3:ListBucket` | config bucket | none |
| 12 | execution | WriteServiceLogs | `logs:CreateLogStream`, `logs:PutLogEvents` | the two log groups `:*` | `:*`: log streams of those two groups; AWS has no finer ARN |
| 13 | execution | ReadDatabasePasswordAndRegistryCredentials | `secretsmanager:GetSecretValue` | the RDS-managed secret, plus the registry secret if set | none |

The orchestrator has no `sts:AssumeRole` on anything but discovery roles, no
`kms:Decrypt` for `aws-deploy`, and no access to the config bucket or its key.
The runner has no `sts:AssumeRole` on anything but deployment roles and no
decrypt for the discovery, Slack or SAML purposes.

### Root example, key policies (`kms.tf`) and bucket policy (`storage.tf`)

| # | Policy | Sid | Actions | Principal / resource | `*` |
|---|---|---|---|---|---|
| 14 | secrets key | EnableIamPolicies | `kms:*` | account root; resource `*` | `kms:*` and root are the statement AWS requires so IAM policies can grant access and the key cannot be orphaned. Resource `*` means this key |
| 15 | secrets key | KeyAdministrationWithoutUse | 16 named management actions, no Encrypt or Decrypt | `var.kms_key_administrator_arns`; resource `*` | resource `*` means this key only |
| 16 | secrets key | OnlyTheRunnerDecryptsDeploymentSecrets (Deny) | `kms:Decrypt`, `kms:ReEncryptFrom` where `purpose` = `aws-deploy` and `aws:PrincipalArn` is not the runner task role | principal `*`; resource `*` | principal `*`: a Deny must catch every caller; resource `*` is this key. It also binds the account root |
| 17 | config-copies key | EnableIamPolicies | `kms:*` | account root; resource `*` | as 14 |
| 18 | config-copies key | KeyAdministrationWithoutUse | same 16 actions | administrators; resource `*` | as 15 |
| 19 | config-copies key | OnlyTheRunnerUsesTheKey (Deny) | `kms:Decrypt`, `kms:ReEncryptFrom`, `kms:GenerateDataKey`, `kms:GenerateDataKeyWithoutPlaintext` unless principal is the runner task role | principal `*`; resource `*` | as 16 |
| 20 | both buckets | DenyInsecureTransport (Deny) | `s3:*` when `aws:SecureTransport` is false | principal `*`; bucket and `bucket/*` | `s3:*` and principal `*`: the point is that no action on any path may use plain HTTP |

### `target-account/` (`iam.tf`)

| # | Role | Sid | Actions | Resources | `*` |
|---|---|---|---|---|---|
| 21 | discovery | trust | `sts:AssumeRole` | principal is the orchestrator task role; `sts:ExternalId` equals the discovery external id | none |
| 22 | discovery | ListClustersAndTaskDefinitions | `ecs:ListClusters`, `ecs:ListTaskDefinitions`, `ecs:DescribeTaskDefinition` | `*` | AWS has no resource-level permissions for these calls. Read-only |
| 23 | discovery | ReadNamedClusters | `ecs:DescribeClusters`, `ListServices`, `DescribeServices`, `ListTasks`, `DescribeTasks` | named clusters, their services and tasks | `/*` after the cluster name: services and tasks of a named cluster |
| 24 | discovery | ReadNamedRepositories | `ecr:DescribeRepositories`, `DescribeImages`, `ListImages` | `var.ecr_repository_arns` | none unless you pass a pattern yourself |
| 25 | deployment | trust | `sts:AssumeRole` | principal is the runner task role; `sts:ExternalId` equals the deployment external id | none |
| 26 | deployment | RegisterAndReadTaskDefinitions | `ecs:RegisterTaskDefinition`, `DescribeTaskDefinition`, `ListTaskDefinitions` | `*` | AWS has no resource-level permissions for registering or listing task definitions |
| 27 | deployment | TagRevisionsOfNamedFamilies | `ecs:TagResource`, `ecs:ListTagsForResource` | `task-definition/<family>:*` for named families | `:*` is the revision number |
| 28 | deployment | UpdateAndObserveNamedServices | `ecs:UpdateService`, `DescribeServices`, `DescribeClusters`, `ListServices`, `ListTasks`, `DescribeTasks` | named clusters, their services and tasks | `/*` as 23 |
| 29 | deployment | ReadNamedRepositories | `ecr:DescribeRepositories`, `DescribeImages`, `ListImages`, `BatchGetImage` | `var.ecr_repository_arns` | as 24 |
| 30 | deployment | ReadAndWriteConfigFiles | `s3:GetObject`, `s3:PutObject` | `<named bucket>/*` | `/*`: objects of a named bucket |
| 31 | deployment | ListConfigBuckets | `s3:ListBucket` | named buckets | none |
| 32 | deployment | PassOnlyTheNamedRolesToEcs | `iam:PassRole` | `var.passable_role_arns` (validated: full ARNs, no wildcard); `iam:PassedToService` = `ecs-tasks.amazonaws.com` | none |

**Totals.** 32 statements. No statement has a `*` action except 14, 17 (the AWS
default key statement) and 20 (a Deny). Resource `*` appears in 22 and 26 (AWS
offers no resource-level control) and in the six key-policy statements, where it
means "this key". Principal `*` appears only in three Deny statements (16, 19,
20). The rest use named ARNs, or `/*`, `:*` suffixes inside a named parent.

**Open points to confirm against the code you run.** (a) The ECS, ECR and S3 actions
in the target account are derived from plan 6; compare with what the runner and
the orchestrator actually call and add or remove accordingly. (b) `ecr:DescribeRepositories`
without a name filter lists everything and fails with a resource-scoped policy;
if the application lists all repositories, pass a repository pattern. (c) The
secrets key serves both services under one key id, because the application reads
a single `YUMDRAKE_KMS_KEY_ID`; separation is therefore by `purpose`, written in
both the roles and the key policy.

## 2. Checklist of the rules

| Rule | Where it is satisfied |
|---|---|
| No account id, region, hostname, certificate, VPC id or identity provider in any file | All come from variables (`variables.tf`, `target-account/variables.tf`). `terraform.tfvars.example` holds `REPLACE-ME` placeholders only. The account id comes from `aws_caller_identity` |
| Each site-specific variable has a description and type; required ones have no default | `variables.tf`. Only sizing, retention and naming variables have defaults |
| `plan` and `apply` never run | `make tf-check` runs `fmt -check` and `init -backend=false` + `validate` only; CI the same, with no credentials |
| Terraform 1.5.7 accepted; provider pinned to a verified range | `versions.tf`: `>= 1.5.7, < 2.0.0`; AWS `~> 5.100.0` (5.100.0 installed by `init`); lock files carry hashes for linux and macOS, amd64 and arm64 |
| Names lowercase with hyphens, consistent | `local.name` (`<prefix>-<environment>`) used by every resource; `name_prefix` and `environment` validated against `^[a-z][a-z0-9-]*$`; bucket names derived from it |
| No secret in any `.tf` or example file | Database password generated and kept by RDS (`manage_master_user_password`); task gets it from Secrets Manager (`PGPASSWORD`). The external ids are `sensitive` variables, passed through `TF_VAR_*`, not in the example file. No key, token or password anywhere |
| Orchestrator can assume only discovery roles; cannot decrypt deployment secrets | Statements 2, 3, 4 and Deny 16 |
| Runner can assume only deployment roles | Statement 7 |
| `iam:PassRole` limited to named task and execution roles | Statement 32, with an ARN validation that refuses wildcards |
| No `*` action or resource except where AWS requires it, each commented | Section 1 above, and a comment next to each in the code |
| KMS separation by encryption-context `purpose` | `locals.tf` lists, statements 3, 4, 8, and Deny 16; the config-copies key is runner-only (Deny 19, statement 9) |
| Orchestrator behind an internal HTTPS load balancer | `load-balancer.tf`: `internal = true`, HTTPS listener on 443 with `certificate_arn`, TLS 1.3 policy, no port 80; the security group admits only `client_cidr_blocks` (never `0.0.0.0/0`, validated) |
| Runner has no inbound rule | `aws_security_group.runner` has no ingress rule and no port mapping in its task definition |
| PostgreSQL and cache not publicly reachable | Private subnets, `publicly_accessible = false`, no public address; ingress only from the two service security groups (`network.tf`) |
| Storage encrypted | RDS `storage_encrypted`; ElastiCache at rest and in transit; S3 SSE-KMS (config copies) and SSE-S3 (logs), both with a TLS-only Deny |
| Config-copies bucket expires objects after 30 days | `storage.tf` lifecycle rule `expire-after-30-days` (and non-current versions after 1 day) |
| RDS backups, point-in-time recovery, deletion protection | `database.tf`: `backup_retention_period >= 1` (validated 1 to 35), `deletion_protection = true`, final snapshot kept |
| Load balancer does not time out the event stream early | `load-balancer.tf`: `idle_timeout = var.load_balancer_idle_timeout_seconds`, default 300, with a comment saying why |
| Secrets never plain values in task definitions | Only `PGPASSWORD` is a secret, via `secrets`/`valueFrom`; every `environment` entry is non-secret (URLs, ARNs, names, switches) |
| Variables from later plans documented | `ecs.tf` comments "read from plan N onward"; listed in the README |
| Shell snippets fail on error | `make tf-check` and the workflow steps start with `set -euo pipefail` (or are single commands run by the shell with `-e` semantics from `||`/`exit 1`) |
