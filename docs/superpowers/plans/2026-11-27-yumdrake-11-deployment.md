# Yumdrake Plan 11: Deployment (Docker Compose and a Terraform Example) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Yumdrake can be run for real in two ways: with Docker Compose on one host, and on ECS from an example Terraform configuration. A third Compose setup, the demo, brings up the whole system with a local AWS (Floci) and sample data so anyone can try it and so the full system is tested end to end.

**Architecture:** The same two images serve every setup; only configuration differs. Compose needs no AWS service of its own: config copies and log archives can live on a volume, encrypted by Yumdrake. The Terraform example shows the ECS setup with separate roles for the two services and for target accounts. It is an example to adapt, validated but never applied here.

**Tech Stack:** Docker Compose, Caddy as an optional TLS front, Terraform (AWS provider), GitHub Actions for images. No new Go module.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 9.4, 15, 19). Owner decisions of 27 November: Terraform is an example with no hosting specifics; the application must also work on Docker Compose.

**Builds on:** Plans 1 to 10.

No full code, by the owner's rule.

## Global Constraints

- All earlier Global Constraints apply. No new Go module.
- Every image reference is pinned to an exact tag or digest. No `latest`.
- Every Compose service has a health check, a restart policy, and memory and CPU limits. No service runs as root. Only the front (or the orchestrator, without a front) publishes a port, and PostgreSQL and Valkey are never published.
- No secret is written into a Compose file, a Terraform file or an image. Compose reads them from an `.env` file that is git-ignored, with a committed `.env.example` holding placeholders only. Terraform takes them as variables marked sensitive or creates them in Secrets Manager.
- Terraform: names in lowercase with hyphens; `terraform fmt` and `terraform validate` must pass; `plan` and `apply` are never run here. Kubernetes conventions in the owner's rules do not apply (there is no Kubernetes).
- Shell scripts start with `set -euo pipefail`, print usage for `-h`, and exit with meaningful codes.
- IAM in the example: the orchestrator's role can assume only discovery roles and cannot decrypt deployment secrets; the runner's role can assume only deployment roles; `iam:PassRole` in a deployment role is limited to named task and execution roles; no `*` actions and no `*` resources except where AWS requires it, each such case commented.
- Config copies are deleted after 30 days in every setup.

## Review Focus

1. A fresh host with only Docker: following the Compose README from top to bottom gives a working sign-in page over HTTPS and a created admin, with no step that needs knowledge the README does not give. (Tasks 2, 6)
2. The host reboots, or `docker compose down` and `up` are run: no run, log, config copy or setting is lost, and a run that was in flight is resumed or reported honestly. (Tasks 2, 6)
3. Upgrading to a new version by changing one variable and running `up -d`: migrations apply once, both services come back, and an older runner is not left talking to a newer schema it cannot use. (Tasks 2, 6)
4. Someone copies the demo setup to a server: it refuses to start outside a development environment, because it enables fake credentials and a known secret key. (Task 3)
5. The Terraform example is adapted by someone who does not know the code: every variable is described, required ones have no default, and `terraform validate` passes on a clean checkout with no AWS credentials. (Task 5)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/artifacts/dirstore.go` | Config copies on a volume, encrypted by Yumdrake |
| `internal/artifacts/retention.go` | Delete copies older than 30 days (directory mode) |
| `deploy/compose/compose.yaml`, `.env.example`, `Caddyfile`, `README.md` | The production Compose setup |
| `deploy/compose/compose.demo.yaml`, `demo/` | The demo: Floci, a fake GitHub, seeded data |
| `cmd/yumdrake-demo/main.go` | Seeds the demo and serves the fake GitHub |
| `scripts/compose-check.sh`, `scripts/demo-check.sh` | Bring a setup up, prove it works, tear it down |
| `.github/workflows/release.yml` | Build and publish images on a version tag |
| `deploy/terraform/example/` | Root example: network inputs, database, cache, buckets, keys, roles, ECS, load balancer, alarms |
| `deploy/terraform/example/target-account/` | Roles to create in each account Yumdrake deploys to |
| `internal/metrics/emf.go` | CloudWatch metrics as embedded-metric log lines |
| `docs/operations.md` | Upgrade, backup and restore, first admin, troubleshooting |

---

### Task 1: Config copies and logs without S3

**Files:** `internal/artifacts/dirstore.go`, `retention.go`; modify `internal/artifacts/artifacts.go` (extract an interface both stores satisfy), `internal/config/config.go`
**Test:** `internal/artifacts/dirstore_test.go`, `retention_test.go`

**Interfaces:**

```go
type Store interface { // the S3 store from Plan 5 already has these methods
    Put(ctx context.Context, runID int64, service, kind, name string, previous bool, data []byte) (key, sha256hex string, err error)
    Get(ctx context.Context, key, wantSHA256 string) ([]byte, error)
}
func NewDir(dir string, c secrets.Cipher) (Store, error)
func Sweep(ctx context.Context, dir string, olderThan time.Duration, now time.Time) (removed int, err error)
```

- Configuration: exactly one of `YUMDRAKE_ARTIFACT_BUCKET` and `YUMDRAKE_ARTIFACT_DIR` must be set for the runner. Likewise `YUMDRAKE_LOG_BUCKET` or `YUMDRAKE_LOG_ARCHIVE_DIR` for the orchestrator (neither means logs stay in PostgreSQL).
- The directory store encrypts every file with the cipher under purpose `artifact`, writes to a temporary name and renames, creates files with mode `0600`, and refuses a key that would leave its directory.
- The runner sweeps copies older than 30 days once an hour. (With S3 the bucket's lifecycle rule does this.)

- [ ] **Step 1: Write the failing tests**: round trip and checksum; bytes on disk are not the plaintext; corrupted file is `ErrCorrupt`; a key with `..` is refused; an interrupted write leaves no partial file under the final name; sweep removes only files older than the limit and empty directories; config validation of the either-or rules.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint test test-integration`; expect pass.
- [ ] **Step 5: Commit** `feat: keep config copies on a volume when there is no S3`.

---

### Task 2: Production Compose setup

**Files:** `deploy/compose/compose.yaml`, `.env.example`, `Caddyfile`, `README.md`, `scripts/compose-check.sh`, `docs/operations.md`; modify `Makefile` (`compose-check`)

Services: `postgres` and `valkey` (same pinned images as development, data on named volumes, Valkey with a password), `orchestrator`, `runner` (two replicas by default through `deploy.replicas`), and under profile `tls` a `caddy` front that terminates HTTPS for the hostname in `.env` and proxies to the orchestrator without buffering the event stream.

- Images come from `${YUMDRAKE_IMAGE_REPO}/yumdrake-orchestrator:${YUMDRAKE_VERSION}` and the runner's equivalent; both variables are required with no default.
- Volumes: database, cache, config copies (runner), log archive (orchestrator).
- The orchestrator runs migrations on start; the runner depends on the orchestrator being healthy, and Plan 1's schema wait covers the rest. (Review Focus 3)
- `.env.example` lists every variable with a one-line description and a placeholder: versions and image repository, public URL, database password, Valkey password, the 32-byte secret key with the command to generate one, optional AWS endpoint override, and the storage directories. Secrets mode is `local`.
- AWS access: the README explains the two supported ways on a single host: an instance role, or an access key for a dedicated IAM user given through `.env`, used by the runner to assume deployment roles and by the orchestrator to assume discovery roles (two different users or roles, as in the design).
- `README.md` steps: prerequisites; copy and fill `.env`; `docker compose up -d`; create the first admin with `docker compose exec` reading the password from the terminal; sign in; connect AWS accounts and GitHub; set the inventory source. Then: upgrade, backup and restore of the database and volumes, logs, troubleshooting by symptom. (Review Focus 1)
- `scripts/compose-check.sh`: builds local images, writes a throwaway `.env` with generated secrets, brings the stack up under its own project name on a free port without the TLS profile, creates an admin, signs in through the API, checks `/readyz`, then `down` and `up` again and confirms the admin can still sign in (Review Focus 2), then tears everything down including volumes. Exit 0 pass, 1 a check failed, 2 usage, 3 Docker unavailable.

- [ ] **Step 1: Write** `scripts/compose-check.sh` first and run it; expect failure because the Compose files do not exist.
- [ ] **Step 2: Create** the Compose setup, `.env.example`, `Caddyfile` and the two documents.
- [ ] **Step 3: Run** `docker compose -f deploy/compose/compose.yaml config -q` with the example values, `shellcheck scripts/compose-check.sh`, and `make compose-check`. Expected: all pass.
- [ ] **Step 4: Check by hand**, and record in the report: no secret value in any committed file (`git grep` for the generated values finds nothing); `docker compose config` shows no published port for PostgreSQL or Valkey; every service has a health check, a restart policy and limits.
- [ ] **Step 5: Commit** `feat: run Yumdrake with Docker Compose`.

Automated unit testing of Compose files is impractical; `compose-check` is the validation.

---

### Task 3: The demo setup

**Files:** `cmd/yumdrake-demo/main.go`, `deploy/compose/compose.demo.yaml`, `deploy/compose/demo/` (sample inventory and jobs), `scripts/demo-check.sh`; modify `Makefile` (`demo`, `demo-down`, `demo-check`), `README.md`
**Test:** `cmd/yumdrake-demo/main_test.go`

`yumdrake-demo` is a development tool, built into its own image and never into the product images. It:
- Seeds Floci using `test/awstest`'s helpers moved to a non-test package they can share: two accounts, five environments, a handful of services running an old version with images for three newer versions (one of which crashes), buckets with env files and TOMLs.
- Serves a fake GitHub with the sample inventory and jobs, including `v<version>` branches and a `dev` branch.
- Then configures Yumdrake through its API as an admin it creates: connections, inventory source, folders, jobs, two more people with different permissions.
- Prints the URL and the three sign-ins.

`compose.demo.yaml` adds Floci (not published on 4566), the demo service, and sets `YUMDRAKE_ENV=dev`, the AWS endpoint, and a fixed, documented demo secret key. Both services refuse to start with that key unless `YUMDRAKE_ENV=dev`, and refuse `YUMDRAKE_ENV=dev` when the public URL's host is not `localhost` or `127.0.0.1`. (Review Focus 4)

`make demo` brings it up and prints where to go. `scripts/demo-check.sh` brings it up, waits for seeding, runs the browser test and the CLI test from Plans 8b, 9 and 10 against it, and tears it down, removing every `floci-aws-*` container it caused.

- [ ] **Step 1: Write the failing tests**: `TestRefusesDemoKeyOutsideDev` and `TestRefusesDevWithPublicHostname` (in `internal/config`); `TestSeedIsIdempotent` (running the seeding twice against the same Floci and Yumdrake changes nothing the second time); then `scripts/demo-check.sh`, failing because the demo does not exist.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint build test test-integration demo-check`. Expected: pass, and no `floci-aws-*` container remains.
- [ ] **Step 5: Commit** `feat: add a self-contained demo with a local AWS`.

---

### Task 4: Metrics and image publishing

**Files:** `internal/metrics/emf.go`; modify `internal/app` (emit every 60 seconds when `YUMDRAKE_METRICS=emf`); `.github/workflows/release.yml`
**Test:** `internal/metrics/emf_test.go`

**Interfaces:**

```go
type Sample struct{ Name string; Value float64; Unit string }
func Write(w io.Writer, namespace string, dims map[string]string, at time.Time, samples []Sample) error // one CloudWatch embedded-metric-format JSON line
func Collect(ctx context.Context, db store.DBTX, now time.Time) ([]Sample, error)
```

- Metrics, namespace `Yumdrake`: `QueueDepth`, `OldestQueuedLegAge` (seconds), `ActiveRunners`, `ExpiredLeases` (last minute), `RunsFinished` and `RunsFailed` (last minute), `RunDuration` (seconds, per finished run), `SyncAge` (seconds), `LogPersistenceFailures`, `NotificationFailures`, `NotificationsPending`. Only the orchestrator emits, and only one of several replicas at a time (advisory lock).
- Release workflow: on a tag `v*`, build both images for `linux/amd64` and `linux/arm64` with the version and commit build arguments and push them to GitHub Container Registry as `:<version>`; actions pinned by commit SHA; `permissions` limited to `contents: read` and `packages: write`; no other secret.

- [ ] **Step 1: Write the failing tests**: `TestWriteProducesValidEMF` (parse the line; `_aws.CloudWatchMetrics` names every sample and dimension; timestamp in milliseconds); `TestCollect` over seeded rows; `TestOnlyOneReplicaEmits`.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement**, and write the workflow. **Step 4: Run** `make lint test test-integration` and `actionlint`; expect pass.
- [ ] **Step 5: Commit** `feat: emit CloudWatch metrics and publish images on release`.

---

### Task 5: The Terraform example

**Files:** everything under `deploy/terraform/example/` and `deploy/terraform/example/target-account/`, each with `README.md`, `versions.tf` (Terraform and provider versions pinned), `variables.tf`, `outputs.tf`, and resources split by concern (`network.tf`, `database.tf`, `cache.tf`, `storage.tf`, `kms.tf`, `iam.tf`, `ecs.tf`, `load-balancer.tf`, `alarms.tf`); modify `Makefile` (`tf-check`), CI

Root example creates, in one account and region given as variables, inside an existing VPC and private subnets given as variables:
- RDS PostgreSQL: encrypted, not public, automated backups with point-in-time recovery, deletion protection, credentials in Secrets Manager, TLS required.
- ElastiCache for Valkey: encrypted in transit and at rest.
- Two S3 buckets, private, encrypted, versioned: config copies with a 30-day expiry, and the log archive with a retention variable.
- Two KMS keys: one the orchestrator may encrypt and decrypt with for its own purposes, one whose decrypt is allowed only to the runner for deployment and config-copy purposes (by encryption context).
- Task roles as in Global Constraints, an execution role, CloudWatch log groups with retention.
- An ECS cluster, two task definitions and services on Fargate, the orchestrator behind an internal HTTPS load balancer using a certificate ARN variable, the runner with no inbound rule, and target-tracking scaling of the runner on `QueueDepth`.
- Alarms on `OldestQueuedLegAge`, `ActiveRunners` below 1, `LogPersistenceFailures` and `NotificationFailures`, to an SNS topic ARN variable.

`target-account/` creates, in an account Yumdrake deploys to: a discovery role (ECS describe and list, ECR describe and list, read-only) and a deployment role (the ECS mutations Plan 6 uses, S3 read and write on named buckets, ECR read, `iam:PassRole` on named roles only), each trusting the matching Yumdrake task role with an external id.

- Every variable has a description and a type; required ones have no default. Outputs give what the other module and the application configuration need.
- `make tf-check` runs `terraform fmt -check -recursive` and, in each of the two directories, `terraform init -backend=false` and `terraform validate`. CI runs it. Nothing is planned or applied. (Review Focus 5)

- [ ] **Step 1: Add** the `tf-check` target and CI step and run it; expect failure because the directory does not exist.
- [ ] **Step 2: Write** the two configurations and their READMEs (what it creates, what it assumes, what to change first, and that it is an example to review before use).
- [ ] **Step 3: Run** `make tf-check`. Expected: pass with no AWS credentials in the environment.
- [ ] **Step 4: Review by hand against Global Constraints**, and record in the report: every IAM statement's actions and resources, each `*` justified in a comment; no secret in any `.tf` or `.tfvars.example`; names in lowercase with hyphens.
- [ ] **Step 5: Commit** `docs: add an example Terraform configuration for ECS`.

Automated testing beyond `fmt` and `validate` is impractical without applying; the hand review in Step 4 is the rest of the validation.

---

### Task 6: Whole-system check

**Files:** modify `.github/workflows/ci.yml` (a `system` job running `make compose-check demo-check`), `README.md` (a "Try it" section pointing at `make demo`)

- [ ] **Step 1: Add** the CI job; run `make compose-check demo-check` locally.
- [ ] **Step 2: Upgrade check** (Review Focus 3): with the production Compose setup running images built from the previous commit, change the version variable to images built from this commit and run `up -d`; confirm migrations applied once, both services are healthy, and a run started before the upgrade is either finished or resumed. Record what was done and seen in the report. If this cannot be automated cheaply, it stays a recorded manual check in `docs/operations.md`.
- [ ] **Step 3: Run** the full suite: `make lint build test test-integration test-floci test-e2e web-lint web-test web-e2e cli-dist compose-check demo-check tf-check floci-clean`. Expected: all pass.
- [ ] **Step 4: Commit** `ci: check the Compose and demo setups end to end`.
- [ ] **Step 5: Push, open a pull request, confirm CI passes.** Do not merge it.

---

## Parallel execution

Tasks 1, 4 and 5 together. Task 2 after Task 1. Task 3 after Task 2. Task 6 last.

## Definition of done

- The command in Task 6 Step 3 passes locally and in CI.
- Every named test exists, was seen failing first, and passes; the two hand reviews are recorded.
- No new Go module.
- A reviewer follows `deploy/compose/README.md` on a clean checkout without help, and confirms the five Review Focus conditions.
