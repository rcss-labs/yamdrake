# Yumdrake design

Date: 27 November 2026
Status: draft for approval
Clickable mockup: https://claude.ai/artifact/5ew1QJFSoieUcSnKzKuP9L (the mockup is the reference for every screen named below)
Mockup source: `docs/mockup/yumdrake-mockup.dc.html`. Its `<style>` block holds the design tokens and component styles, its markup holds every screen's layout, fields and filters, and its script holds the sample data and behaviour.

Calls marked **(call)** were made while writing this document and were not decided in conversation. They are the ones to check first.

## 1. Intent

Deployments run on Jenkins today. It is slow, the Groovy setup is large, and a general CI server is the wrong tool for what is a simple orchestration: copy config files, then roll a new image version onto ECS services.

Yumdrake is a slim, purpose-built deployment tool. DevOps describe jobs and inventory in YAML. QA, developers and DevOps run those jobs from a web page or a CLI, watch them live, and read the full log afterwards. GitHub Actions keeps building images. Yumdrake only deploys versions that already exist.

## 2. Goals, non-goals, anti-goals

**Goals**

- A job is a YAML file. Its page (options, version pickers, plan) is generated from that file.
- One environment per run. A job deploys one service or many, grouped so one version can be picked for a whole group, with a per-service override.
- Inventory in YAML: one `environments.yaml` and one file per service holding its complete setup for every environment it exists in.
- A run is a graph of built-in steps with dependencies. A failed sequential step stops what depends on it. Parallel legs continue.
- Every run keeps its history, a failure summary that names what failed and why, and the full console log of every step and service, including AWS error messages.
- SAML single sign-on from the start, local accounts for break-glass and service use, and permissions scoped by environment label, all managed in the application.
- Optional, per-job approvals and Slack notifications.
- Visibility: an overview with analytics, and a matrix of which version runs where.

**Non-goals**

- Building images or anything else CI does.
- User-defined step types. Steps are built in. One built-in step runs reviewed commands in a container.
- Automatic rollback. A failed service stays on what ECS keeps running. Going back is a deliberate run of the old version.
- Scheduled runs and email notifications.
- Cancelling a run in progress **(call)**. Timeouts bound a stuck run in version 1.
- Editing YAML in the application. YAML is edited in git and is read-only in the UI.

**Anti-goals**

- Rebuilding Jenkins: arbitrary scripting as the main way to define a job.
- Permissions or users defined in YAML.
- A run view that shows only green and red boxes without the log behind them.

## 3. Constraints

Decided by the owner:

- Runs on ECS. PostgreSQL on RDS is the database. Valkey is the cache.
- Go backend. React frontend.
- Two backend services: an **orchestrator** and a **runner**.
- Inventory and jobs are YAML in GitHub. Users, permissions, connections and history live in PostgreSQL, encrypted at rest.
- Terraform is planned and applied by the hosted Terraform runner, never locally. Local work is `terraform fmt` and `terraform validate`.

## 4. Approaches

The stack and the two-service split were given. These are the choices that remained. Each was scored with the TypeSafe Jev model (`jev-1.14.0`) on a neutral description of the system; the probability it gave each option is in brackets.

**How the orchestrator hands work to the runner**

- PostgreSQL queue: work items are rows, claimed with `SELECT ... FOR UPDATE SKIP LOCKED` and a lease. Valkey pub/sub is only a wake-up signal. Nothing is lost if the cache is flushed, and there is no extra moving part. (0.86)
- Amazon SQS between the services: durable and managed, but a third system to operate and a second source of truth for run state. (0.12)
- Valkey Streams as the queue: fast, but run state would depend on a cache. (0.02)
- Direct calls from orchestrator to runner: the orchestrator would have to track runner instances and in-flight calls itself. (0.00)

Chosen: PostgreSQL queue.

**What one unit of work is**

- One task per leg: the orchestrator owns the graph and dispatches one small task per step leg, for example one service rollout. Any runner takes any task. A crashed runner loses only the legs it held. (0.82)
- One task per step, with the runner fanning out the services inside it. (0.15)
- A runner claims a whole run and holds the graph in memory. (0.02)

Chosen: one task per leg.

**Where full logs are stored**

- Live lines through Valkey Streams, finished logs as compressed objects in S3, an index in PostgreSQL. Cheap to keep for years, no size limit on a script's output, the database stays small. Costs a second store and a merge when reading. (0.82)
- Rows in a PostgreSQL table. One store and simple queries, and adequate at this volume, but the database carries every log line forever. (0.12)
- CloudWatch Logs read back through its API. (0.05)

Chosen: Valkey Streams for live, S3 for durable. The PostgreSQL option is a reasonable simpler alternative and the one to switch to if the owner prefers fewer stores.

**How the browser gets live updates**

- Server-Sent Events (0.97), WebSocket (0.02), polling (0.01).

Chosen: Server-Sent Events. One-way is all that is needed, it passes a normal load balancer, and it reconnects on its own.

**Where the script step runs**

- A short-lived, isolated Fargate task with its own low-privilege role (1.00), or inside the runner's own container (0.00).

Chosen: a separate Fargate task.

## 5. System overview

```
 Browser (React)        CLI (yumdrake)
        \                 /
         HTTPS, REST + SSE
               |
        +--------------+        GitHub (YAML, env files)
        | orchestrator |------> Slack
        +--------------+------> AWS read-only (ECR tags, ECS versions)
          |    |    |
   PostgreSQL  Valkey  S3 (logs, artifacts)
          |    |    |
        +--------------+
        |    runner    |------> AWS per environment account (S3, ECS, ECR)
        +--------------+------> GitHub (env files)
                        ------> Fargate task for the script step
```

**Orchestrator** (ECS service, two or more tasks behind an internal HTTPS load balancer)

- Serves the REST API, the SSE streams and the built React app (embedded in the binary) **(call)**.
- Sign-in (SAML and local), sessions, CLI tokens, permission checks, audit log.
- Reads and validates YAML from GitHub and keeps the last good version of each file.
- Creates runs, holds the step graph, decides what is ready, handles approvals, sends Slack messages.
- Read-only AWS lookups for the UI: ECR tags for version pickers, ECS for the Versions page.
- Never changes anything in an environment.

**Runner** (ECS service, two or more tasks, no inbound traffic)

- Claims leg tasks from PostgreSQL, executes them, writes status and log lines.
- The only component that changes environments: S3 writes, ECS task definitions and service updates, launching script tasks.
- Stateless. Any runner can resume a leg another runner dropped.

Both services are built from one Go module and share packages for the database, YAML model, AWS access and logging.

## 6. YAML model

### 6.1 Environments: one file

`environments.yaml` at the inventory path of the config repository.

```yaml
environments:
  - name: hetzner-01
    label: dev            # dev | preprod | prod
    aws_account: "111111111111"
```

- `name` is unique. `label` is one of `dev`, `preprod`, `prod`. `aws_account` must match an AWS account connected in Admin.
- Nothing else lives on an environment.

### 6.2 Services: one file per service

`services/<name>.yaml`. Each file holds a block per environment the service exists in. All paths are complete. There are no variables.

```yaml
name: backend-api
environments:
  hetzner-01:
    ecs_service: arn:aws:ecs:eu-central-1:111111111111:service/hetzner-01/backend-api
    ecr_repository: 111111111111.dkr.ecr.eu-central-1.amazonaws.com/backend-api
    env_files:                       # optional, any number
      - name: backend-api.env
        source_env_path: example-org/env-config-repo/hetzner-01/backend-api/backend-api.env
        destination_env_path: s3://hetzner-01-config/env/backend-api/backend-api.env
    tomls:                           # optional, any number
      - name: backend-api.toml
        source_toml_path: s3://main-config-bucket/hetzner-01/backend-api.toml
        destination_toml_path: s3://hetzner-01-config/toml/backend-api.toml
```

- `ecs_service` is a full service ARN. `ecr_repository` is a full repository URI. Region and account are taken from them.
- The ECR repository may be in a different AWS account from the environment (dev environments use the dev registry, preprod and prod use the prod registry). That account must also be connected in Admin.
- `source_env_path` is `<owner>/<repo>/<path in repo>`. It names no branch (see 8.1).
- A service missing from an environment is greyed out on job pages for that environment and left out of the run.

### 6.3 Jobs: one file per job, in any repository

```yaml
job: deploy-backend
title: "Backend and microservices"

environment:
  labels: [dev, preprod, prod]       # which environments the picker offers

groups:                              # the services this job deploys
  backends:
    label: Backend services
    services: [backend-socket, admin-history, backend-api, backend-worker]
  microservices:
    label: Microservices
    services: [orders, billing, catalog]

approval:                            # optional
  when:
    labels: [prod]
  expires: 24h

notify:                              # optional
  slack: "#deployments"

steps:
  - name: config-backends
    use: copy-config
    group: backends
    needs: []
  - name: roll-backends
    use: ecs-deploy
    group: backends
    needs: [config-backends]
  - name: config-microservices
    use: copy-config
    group: microservices
    needs: []
  - name: roll-microservices
    use: ecs-deploy
    group: microservices
    needs: [config-microservices]
```

A script job declares `inputs` instead of `groups`:

```yaml
job: publish-report-tool
title: "Publish the report tool"
environment:
  labels: [dev]
inputs:
  - name: version
    type: text
    label: Version
steps:
  - name: build-report-tool
    use: run
    image: python:3.12-slim
    commands:
      - pip install -r requirements.txt
      - ./build.sh
    outputs:
      - zip: dist/
        to: s3://artifacts-bucket/report-tool/${inputs.version}.zip
```

Rules:

- `job` is the job's id, unique across Yumdrake. A group with one service is a single-service job.
- Step dependency rule **(call)**: a step with no `needs` key waits for the step above it, so a plain list runs top to bottom. `needs: []` means "wait for nothing", which is how parallel flows start. `needs: [a, b]` waits for the named steps.
- A file is rejected when it loads if it has an unknown step type, an unknown service, a circular or misspelled dependency, or a duplicate name. The last good version stays in use and the error is shown on the job page.
- `${inputs.<name>}` is the only substitution, and only inside the `run` step. Inventory files have none.
- The job's folder is not in the YAML. Folders are created and assigned in the application.

## 7. Runs

### 7.1 Starting a run

The user picks one environment, ticks groups or single services, and picks a version per group with optional per-service overrides. The page shows a plan before starting.

- Version lists come from the ECR repository each service has set for that environment. A group's list is the tags present in every selected service's repository. A service override lists that service's own tags. Lists are cached in Valkey for 60 seconds.
- Which tags count as versions **(call, see 19)**: tags that look like a version, `2.212.0` or `v2.212.0`, with an optional suffix such as `-rc1`. Other tags (`latest`, commit hashes) are not offered. Newest first by version number.
- "Re-run failed" **(call)**: a failed run offers a button that opens the job's Run tab with the same environment, only the failed and skipped services ticked, and the same versions. It starts a new run that records which run it came from.
- The orchestrator checks the user may run jobs in the environment's label, then stores the run together with a snapshot of the job definition and of every service block it will use. A run never changes because YAML changed after it started.
- If the job asks for approval in that label, the run waits (see 10). Otherwise it is queued.

### 7.2 State

- Run: `waiting_approval`, `queued`, `running`, `succeeded`, `failed`, `rejected`, `expired`.
- Step: `pending`, `running`, `succeeded`, `failed`, `skipped`.
- Leg: `pending`, `queued`, `claimed`, `running`, `succeeded`, `failed`, `skipped`.

A step with `group` has one leg per selected service. A step without one has a single leg.

### 7.3 Scheduling and failure rules

- A step becomes ready when every step it needs has succeeded. All its legs are queued at once and run in parallel.
- A failed leg does not stop its sibling legs. The step is `failed` once all legs have finished and at least one failed.
- A step that needs a failed or skipped step is `skipped`, and so is everything downstream of it. Unrelated branches carry on.
- Dependencies are between whole steps, as written here. Whether they should instead be per service when two steps cover the same group is the first open question in 19, and it must be answered before plan 3.
- The run is `failed` if any step failed, else `succeeded`.
- Environment lock **(call)**: a leg that changes a service waits while another run holds the same service in the same environment. The run page says what it is waiting for.
- The scheduler is a function of database state. Any orchestrator task may advance a run; it takes a row lock on the run while it does, so two tasks never queue the same leg twice.

### 7.4 Queue between orchestrator and runner

- A queued leg is a row. A runner claims it with `FOR UPDATE SKIP LOCKED`, sets a lease, and renews the lease while it works.
- The orchestrator publishes a wake-up on Valkey when it queues legs, and the runner publishes one when a leg finishes. Both also poll every few seconds, so a lost message only costs a short delay.
- A leg whose lease expired is returned to the queue. Legs are written to be resumed, not restarted: the runner records the task definition it registered, the ECS deployment id, or the script task ARN as soon as it has them, and the next runner picks up from there.
- A leg that has not started changing anything may be retried. A leg that failed is not retried automatically.

### 7.5 Logs

- Every line has: run, step, service (if any), sequence number, timestamp, level (`info`, `ok`, `warn`, `error`) and text.
- Runners write lines to a Valkey Stream per run for live viewing and flush them to S3 as compressed NDJSON chunks at least every 5 seconds and when a leg ends. PostgreSQL holds an index of chunks.
- The orchestrator serves a run's log by merging S3 chunks with the live stream, filtered by step, service, level or text. The browser follows a running log over SSE.
- If Valkey is flushed during a run, at most the last few seconds of lines are lost. Run state is unaffected.
- Every AWS and GitHub call a step makes is logged with its outcome. Errors keep the full message and request id. Secrets are never logged.
- Each failed leg also stores a short reason and a full reason in PostgreSQL. These feed the "What failed" panel, the history tables and Slack, without reading the log.

## 8. Built-in steps

### 8.1 `copy-config`

For each selected service in the group, in the chosen environment:

- Env files: resolve the branch, read each `source_env_path` from GitHub at that branch, and put it at `destination_env_path`.
  - The branch is `v` plus the version being deployed for that service, for example `v2.212.0`. A version that already starts with `v` is used as it is.
  - If that branch does not exist in the repository, the branch is `dev` and the log says so as a warning.
  - A file missing from the branch that was chosen fails the leg. There is no per-file fallback.
- TOMLs: copy each `source_toml_path` to `destination_toml_path` (S3 to S3).
- A service with neither is logged as "nothing to copy" and succeeds.

One leg per service. Copies are idempotent.

Credentials **(call)**: both S3 reads and writes use the AWS connection of the environment's account, so that account's role must be allowed to read the source bucket (a bucket policy on the source side if it is in another account). Env files are read with the GitHub connection whose owner matches the first part of `source_env_path`.

### 8.2 `ecs-deploy`

For each selected service:

1. Describe the ECS service and its current task definition.
2. Confirm the image tag exists in the service's ECR repository.
3. Register a new task definition revision identical to the current one except for the image tag of every container whose image is in that repository.
4. Update the service to the new revision.
5. Wait until ECS reports the rollout complete or failed, logging ECS service events as they appear. Default timeout 15 minutes, settable per step with `timeout`.
6. On failure, log the stopped tasks' reasons and exit codes and record the reason.

Yumdrake does not roll back. If the ECS deployment circuit breaker is enabled on the service, ECS rolls back by itself and the log shows it.

### 8.3 `run`

Runs the listed commands in the named image as a Fargate task in Yumdrake's own AWS account, with a dedicated task role.

- Task output is streamed into the run log.
- `outputs` are collected by a small Yumdrake sidecar container that shares the working directory, waits for the commands to finish, then zips and uploads **(call)**. The user's image needs no extra tooling.
- The exit code decides success.
- Yumdrake never builds or pushes images.

Not yet settled for this step: where the commands' source code comes from, what the chosen environment means for a script job beyond permissions, and whose credentials write the outputs. These are in 19 and need answers before plan 9, not before the earlier plans.

## 9. Sign-in and permissions

- **SAML** is the normal way in. Groups arrive in a SAML attribute and are mapped to Yumdrake groups in Admin.
- **Local accounts** exist for a break-glass admin and for service accounts. Passwords are stored as argon2id hashes.
- **Sessions** are server-side in Valkey behind a secure, http-only cookie.
- **CLI tokens** belong to a user, carry that user's permissions and no more, are stored hashed, and expire. `yumdrake login --sso` opens the browser and receives a token when sign-in completes.
- **Permissions** are grants to a group: an action (`run`, `approve`) for an environment label, optionally narrowed to a folder **(call)**. A group may instead be a global admin. Everyone signed in may view.
- The UI hides or disables what a user may not do. The API enforces it regardless.
- **Audit log**: every permission change, connection change, job or folder change, run request, approval and rejection, with who and when.

Secrets Yumdrake must read back (AWS access keys, GitHub App key or token, Slack token, SAML key) are encrypted with a KMS key before they are stored. RDS storage is encrypted and the connection requires TLS.

## 10. Approvals and Slack

- A job asks for approval only where its YAML says so.
- Who may approve is a permission, not YAML. Nobody approves their own run unless they are a global admin.
- A waiting run expires after `approval.expires` (default 24 hours).
- Approve and reject happen in Yumdrake, on the run page or the Approvals page. Slack carries a link.
- For a job with `notify.slack`, Slack gets: the approval request, the decision, the start, and the result with what failed and why.
- The Slack connection (a bot token) is set in Admin. This screen is not in the mockup yet.

## 11. Connections and sync

- **AWS accounts**, set in Admin: a role to assume (preferred) with an external id, or an access key and secret. The runner uses the connection for the environment's account to make changes. The orchestrator uses the same connections for read-only lookups.
- **GitHub**, set in Admin: a GitHub App (preferred) or a token, read-only. Used for YAML and for env files. There may be several, one per organisation or owner; the owner in a repository path picks the connection.
- **Inventory source**: one repository, branch and path, set in Admin.
- **Jobs** are added in the UI by pointing at a repository, branch and path. The folder is chosen there.
- **Sync** **(call)**: polling is the baseline, because the load balancer is internal and GitHub cannot reach it. Every file is checked every minute, and a "Re-read" button does it at once. A GitHub push webhook is an optional addition for when a public endpoint exists. Each file's last good version, commit and any validation error are stored.

## 12. Web application

React with TypeScript, built with Vite, served by the orchestrator. Server state through a query library with SSE for live data. Styling follows the mockup's tokens: IBM Plex Sans and Mono, neutral surfaces, colour reserved for status, status always shown with a shape and a label as well as colour.

Screens, as in the mockup:

- **Overview**: runs, success rate, median duration and waiting approvals for the last 14 days; runs per day; services that failed most; needs attention; last run per environment. Filter by label.
- **Jobs**: one table grouped by folder, with each job's last run. Admins add jobs, create folders and move jobs.
- **Job**: facts row, then History (filters, service strip, what failed), Run (environment, groups, versions, plan) and Definition (YAML and steps).
- **Run**: facts, approval banner, "What failed", then steps and services on the left and the console on the right with text filter and errors-only.
- **Runs**: every run, with search and filters.
- **Approvals**: runs waiting, with approve and reject.
- **Versions**: service by environment matrix of running versions, read from ECS and cached for 2 minutes, flagging environments behind others with the same label and services whose last deploy failed.
- **Inventory**: environments and services, read-only, with the YAML.
- **CLI**: usage and the user's tokens.
- **Admin**: users, roles, single sign-on, AWS accounts, GitHub, Slack, git sources, audit log.

The **service strip** (one cell per service in a run, failed cells taller and red) appears wherever a run is listed.

## 13. API and CLI

- REST and JSON under `/api/v1`, plus SSE endpoints for a run's status and log. The web app and the CLI use the same API.
- Resource groups: session and tokens; jobs and folders; inventory; versions (ECR tags, running versions); runs, steps, legs and logs; approvals; analytics; admin (users, groups, grants, connections, sources, audit).
- CLI commands: `login`, `jobs list`, `run <job> --env <name> --set <group>=<version> --set <group>.<service>=<version> --only <groups or services>`, `runs watch <id>`, `versions <service> --env <name>`, `approvals approve|reject <id>`, `validate <path>`.
- `validate` checks inventory and job files locally with the same validator the orchestrator uses, so it can run in the config repository's CI.

## 14. Data

PostgreSQL holds:

- People and access: users, local credentials, groups, identity-provider group mappings, grants, CLI tokens, audit log.
- Connections and settings: AWS accounts, GitHub connections, Slack connection, SAML settings, inventory source. Secrets encrypted.
- Definitions: folders, jobs (source, last good definition, sync status), inventory snapshot (environments and services as parsed, with commit).
- Runs: runs (with definition snapshot and selections), steps, legs (also the work queue: status, lease, runner, resume data, failure reasons), approvals, log chunk index, service locks.

Valkey holds only what can be lost: sessions, version-list and Versions caches, live log streams, wake-up messages.

S3 holds log chunks and script outputs.

## 15. Deployment

- ECS on Fargate: `yumdrake-orchestrator` behind an internal HTTPS load balancer, `yumdrake-runner` with no inbound access.
- RDS PostgreSQL, ElastiCache for Valkey, one S3 bucket, one KMS key, database credentials in Secrets Manager.
- Runner task role may assume the connected roles in other accounts and launch script tasks. Orchestrator task role has no write access to any environment.
- Terraform lives in this repository under `deploy/terraform`, named in lowercase with hyphens.
- Images: multi-stage builds, pinned base tags, a `HEALTHCHECK`. Both services expose liveness and readiness endpoints and log structured JSON.
- Database migrations are versioned files applied by the orchestrator on start, guarded by a lock.

## 16. Testing

- Test first for behaviour: write the failing test, see it fail for the right reason, then implement.
- Unit tests: YAML validation, the dependency rule and scheduler (every failure and skip case in 7.3), permission checks, branch choice for env files, task-definition rewrite.
- Integration tests against real PostgreSQL and Valkey in containers: claiming and leases, resuming a dropped leg, log write and merged read, two schedulers on one run.
- AWS and GitHub sit behind interfaces with fakes. The fakes return the real error shapes (AccessDenied, health-check failure) so failure reasons and logs are tested.
- Web: component tests for the run page and job page, and a small browser test of the main path: sign in, run a job, watch it fail, open the failed service's log.

## 17. Order of delivery

Each is its own implementation plan. Each ends with something that runs.

1. **Foundations**: repository layout, Go module, configuration, migrations, local environment (PostgreSQL, Valkey, fake AWS), CI, images.
2. **YAML and sync**: schemas and validator, GitHub connection, inventory and job sync, read API, `yumdrake validate`.
3. **Run engine**: runs, steps, legs, scheduler, queue, runner with a fake step, logs to Valkey and S3, SSE.
4. **AWS steps**: account connections, version lists from ECR, `copy-config`, `ecs-deploy`.
5. **Sign-in and permissions**: local accounts, SAML, groups and grants, CLI tokens, audit log.
6. **Web application**: shell, Jobs, Job, Run, Runs, Inventory, Admin.
7. **Approvals and Slack**.
8. **Overview and Versions**.
9. **`run` step and the rest of the CLI**.
10. **Terraform and first deployment**.

A local admin account exists from plan 1 so later plans can be exercised before plan 5 lands.

## 18. Left to the builder

- Go libraries for routing, database access, migrations, SAML and Valkey. Each new dependency is weighed for maintenance, security and supply-chain risk before it is added.
- Exact table columns, index choices and API paths, within the shapes above.
- Chunk size and flush timing for logs, lease length and poll intervals.
- The React component breakdown and the query library.
- Whether charts are hand-built as in the mockup or use a small library.

## 19. Open for the owner

Needed before the plans can be written:

1. **Per-service or per-step dependencies** (before plan 3). `roll-backends` needs `config-backends`. If the config copy fails for one backend service only, should the other backends still roll (each service's rollout waits only for its own config), or should no backend roll (the whole step failed)? This document assumes the second.
2. **Image tag format** (before plan 4). Do your ECR tags look like `2.212.0` or `v2.212.0`, and are there other tags in the repositories that must not be offered as versions?

Needed later:

3. **The `run` step** (before plan 9). Where the commands' code comes from (a repository and ref named in the YAML?), whether the chosen environment means anything for a script job, and which account and credentials own the output bucket.
4. **Hosting** (before plan 10). AWS account and region for Yumdrake itself, whether a VPC and private subnets exist, the hostname, DNS zone and certificate, and which SAML identity provider is used.

Calls to confirm or change:

5. Log storage: Valkey plus S3 as written, or PostgreSQL only for fewer stores.
6. The dependency rule in 6.3: no `needs` means "after the step above", `needs: []` means "start at once".
7. The environment lock in 7.3: a second run touching the same service in the same environment waits.
8. No cancel button in version 1.
9. A "Re-run failed" button in version 1.
10. Permissions narrowed by folder as well as by label.
11. YAML sync by polling every minute, with webhooks optional.
12. Product name: the repository is `yamdrake`, the mockup and this document say Yumdrake.
