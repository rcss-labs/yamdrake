# Yumdrake design

Date: 27 November 2026
Status: revision 3, draft for approval. Revised after the architecture review in `docs/reviews/2026-11-27-design-review.md` and the owner's decisions recorded in section 19.
Clickable mockup: https://claude.ai/artifact/5ew1QJFSoieUcSnKzKuP9L
Mockup source: `docs/mockup/yumdrake-mockup.dc.html`. Its `<style>` block holds the design tokens and component styles, its markup holds each screen's layout, fields and filters, and its script holds sample data and behaviour. The mockup predates this revision; section 12 lists the screens it does not have yet. The repository is named `yamdrake`; the product is Yumdrake.

Calls marked **(call)** were made while writing and were not decided in conversation. Section 19 records what the owner decided and what is still open. Appendix A records what was done with each review finding.

## 1. Intent

Deployments run on Jenkins today. It is slow, the Groovy setup is large, and a general CI server is the wrong tool for what is a narrow job: put the right config files in place, then roll a new image version onto ECS services.

Yumdrake is a purpose-built deployment tool for ECS. DevOps describe jobs and inventory in YAML. QA, developers and DevOps run those jobs from a web page or a CLI, see exactly what a run will change before it starts, watch it live, and read the full log afterwards. GitHub Actions keeps building images. Yumdrake only deploys versions that already exist.

Yumdrake owns the whole deployment lifecycle, not only the execution of steps: what exactly will be deployed, who may approve it, what else is touching the same services, whether it worked, and how to put things back.

## 2. Goals, non-goals, anti-goals

**Goals**

- A job is a YAML file. Its page (options, version pickers, plan) is generated from that file.
- One environment per run. A job deploys one service or many, grouped so one version can be picked for a whole group, with a per-service override.
- Inventory in YAML: one `environments.yaml` and one file per service holding its complete setup for every environment it exists in.
- A run executes exactly what was planned and approved. Its inputs are pinned before approval and cannot drift.
- Two runs never interleave on the same service or the same config destination.
- A run is a graph of built-in steps with dependencies. Parallel legs continue when a sibling fails.
- A run survives a crash or redeploy of either service. Outcomes that cannot be confirmed are reported as unknown, never as success.
- Every run keeps its history, a failure summary naming what failed and why, and the full console log of every step and service, including AWS error messages.
- A running deployment can be cancelled safely. A service can be restored to exactly what it ran before, including its config files.
- SAML single sign-on from the start, local accounts for break-glass and service use, and permissions managed in the application.
- Per-job approvals and Slack notifications, and deployment freezes set by admins.
- Visibility: an overview with analytics, and a matrix of which version runs where.

**Non-goals**

- Building images or anything else CI does.
- Deploying frontend applications. Yumdrake's own React UI is in scope; deploying other frontends is not.
- User-defined step types. Steps are built in.
- In version 1: a step that runs scripts in a container, a separate verify step with HTTP and alarm checks, and admin-set environment policies that override job YAML. The owner moved these to after the first release (section 19).
- Automatic rollback. Yumdrake restores only when a person asks.
- Scheduled runs and email notifications.
- Editing YAML in the application. YAML is edited in git and is read-only in the UI.
- Reusable job templates, drift detection beyond the Versions page, and GitHub webhooks. These can follow version 1.
- Kubernetes, a general workflow language, or further services beyond the orchestrator and the runner.

**Anti-goals**

- Rebuilding Jenkins: arbitrary scripting as the main way to define a job.
- Permissions or users defined in YAML.
- A run view that shows only green and red boxes without the log behind them.
- Reporting success for something Yumdrake could not confirm.

## 3. Constraints

Decided by the owner:

- Runs on ECS. PostgreSQL on RDS is the database. Valkey is the cache.
- Go backend. React frontend.
- Two backend services: an **orchestrator** and a **runner**.
- Inventory and jobs are YAML in GitHub. Users, permissions, connections and history live in PostgreSQL, encrypted at rest.
- Terraform is planned and applied by the hosted Terraform runner, never locally. Local work is `terraform fmt` and `terraform validate`.

## 4. Decisions

Each records what was chosen, what else was considered, and how the choice fails.

**D1. The work queue is PostgreSQL.**
Legs are rows, claimed with `FOR UPDATE SKIP LOCKED` under a lease. Valkey pub/sub is only a wake-up signal.
Considered: SQS (a third system and a second source of truth for run state), Valkey Streams (run state would depend on a cache), direct calls from orchestrator to runner (the orchestrator would track runner instances and in-flight calls).
Failure mode: a slow or unavailable database stops scheduling. That is acceptable because nothing can be recorded without it anyway. Polling load is small at this scale.

**D2. One unit of work is one leg.**
The orchestrator owns the graph and dispatches one task per step per service. Any runner takes any leg.
Considered: one task per step with fan-out inside the runner, or a runner holding a whole run in memory.
Failure mode: more rows and more scheduling passes. A crashed runner costs only the legs it held.

**D3. Execution is at-least-once with reconciliation.**
PostgreSQL leases cannot make an AWS call happen exactly once. Every step therefore defines how it recognises and adopts work a previous attempt already did (section 8), and an outcome that cannot be determined becomes `unknown`.
Failure mode: a stale runner may complete an AWS call after losing its lease. Steps are written so that a repeated or late call is harmless or detectable.

**D4. A run executes a pinned manifest.**
Before approval, every input is resolved and frozen: image digests, git commits, config file contents, current task definitions (section 7.2). Approval is bound to the manifest's identity.
Considered: snapshotting only the YAML, which leaves tags, branches and S3 objects free to change between approval and execution.
Failure mode: copies of config files, which may contain secrets, are held in Yumdrake's own bucket. They are encrypted, access-restricted and deleted after 30 days (section 9.4).

**D5. Locks cover the whole deployment, per target resource.**
A run takes locks on every ECS service ARN and every config destination it will touch, all at once, before its first change, and holds them until the run is finished and confirmed.
Considered: locking per leg, which lets run A copy config, run B overwrite it, and run A then roll its image onto B's config.
Failure mode: a run stuck in an unknown state keeps its locks until someone reconciles it. That is intended. An audited admin action can release them.

**D6. Logs are acknowledged in PostgreSQL, archived to S3, announced through Valkey.**
A log line counts as stored once its batch is committed to PostgreSQL. Finished legs are moved to compressed objects in S3. Valkey only tells listeners that new lines exist.
Considered: Valkey Streams as the live store with periodic S3 flushes (loses recent lines if the cache is flushed, which contradicts "full log"), PostgreSQL only (the database carries every line forever), CloudWatch Logs.
Failure mode: lines a runner produced but had not committed when it died are lost. The next attempt writes a gap marker so the log says so.

**D7. Live updates use Server-Sent Events.**
One-way is all that is needed, it passes a normal load balancer, and `Last-Event-ID` lets a reconnecting browser resume from durable storage.
Considered: WebSocket, polling.

**D8. The orchestrator and the runner use different AWS roles.**
The orchestrator assumes a read-only discovery role. The runner assumes a deployment role. Script tasks get their own narrow role.
Considered: one connection per account for both, which would give the API-facing service write access to every environment.

**D9. No script step in version 1.**
Running commands in a container is a separate security boundary. The owner moved it to after the first release, where it gets its own design.

## 5. System overview

```
 Browser (React)        CLI (yumdrake)
        \                 /
         HTTPS, REST + SSE
               |
        +--------------+        GitHub (YAML)
        | orchestrator |------> Slack
        +--------------+------> AWS discovery roles (read-only: ECR tags, ECS state)
          |        |
     PostgreSQL   Valkey
          |        |
        +--------------+------> AWS deployment roles (S3, ECS, ECR)
        |    runner    |------> GitHub (env files)
        +--------------+------> S3 (manifest artifacts, log archive)
```

**Orchestrator** (ECS service, two or more tasks behind an internal HTTPS load balancer)

- REST API, SSE streams, and the built React app embedded in the binary **(call)**.
- Sign-in, sessions, CLI tokens, permission and policy checks, audit log.
- Reads and validates YAML from GitHub.
- Creates runs, holds the step graph, takes and releases locks, decides what is ready, handles approvals, cancellation, freezes and notifications.
- Read-only AWS lookups for the UI through discovery roles.
- Never changes anything in an environment and never reads config file contents.

**Runner** (ECS service, two or more tasks, no inbound traffic)

- Claims legs from PostgreSQL, executes them, writes status and log lines.
- Resolves manifests and runs preflight, because those need to read config sources.
- The only component that changes environments.
- Stateless. Any runner can resume a leg another runner dropped.

Both are built from one Go module and share packages for the database, the YAML model, AWS access and logging.

## 6. YAML model

Every file carries a schema version and a kind. Unknown fields and invalid references are rejected. JSON Schemas for all three kinds are published from the repository for editor completion and CI.

### 6.1 Environments: one file

```yaml
apiVersion: yumdrake/v1
kind: Environments
environments:
  - name: hetzner-01
    label: dev            # dev | preprod | prod
    aws_account: "111111111111"
```

`name` is unique. `aws_account` must match a connected AWS account. Nothing else lives on an environment. Freezes are set in the application, not here.

### 6.2 Services: one file per service

```yaml
apiVersion: yumdrake/v1
kind: Service
name: backend-api
versions:                            # optional; this is the default
  pattern: '^v?\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$'
  sort: semver                       # semver | pushed
  limit: 100
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

- One block per environment the service exists in. All paths are complete. There are no variables.
- `ecs_service` is a full service ARN and `ecr_repository` a full repository URI. Region and account come from them, so a job may span regions.
- The ECR repository may be in a different AWS account from the environment. That account must also be connected.
- `source_env_path` is `<owner>/<repo>/<path>`. It names no branch (section 8.1).
- `versions` decides which image tags are offered and how they are ordered.
- A service missing from an environment is greyed out on job pages for it and left out of the run.

### 6.3 Jobs: one file per job, in any repository

```yaml
apiVersion: yumdrake/v1
kind: Job
job: deploy-backend
title: "Backend and microservices"

environment:
  labels: [dev, preprod, prod]

groups:
  backends:
    label: Backend services
    services: [backend-socket, admin-history, backend-api, backend-worker]

approval:                            # optional
  when:
    labels: [prod]
  expires: 24h

notify:
  slack: "#deployments"

steps:
  - name: config-backends
    use: copy-config
    group: backends
  - name: roll-backends
    use: ecs-deploy
    group: backends
    max_parallel: 2                  # optional
    needs:
      - step: config-backends
        scope: service               # this service's rollout waits only for its own config
```

Dependency rules:

- `needs` is always explicit, as in GitHub Actions. A step without `needs` starts at once.
- `needs: [a]` or `{step: a, scope: step}` is a whole-step barrier: the step runs only if every leg of `a` succeeded.
- `{step: a, scope: service}` is per service: each service's leg waits only for the same service's leg in `a`. Both steps must use the same group. This is the normal choice for independent services.
- A barrier does not make a deployment atomic. Services that already rolled stay rolled.
- A file is rejected if it has an unknown step type or service, a circular or misspelled dependency, a service-scoped dependency across different groups, or a duplicate name.
- The job's folder is not in the YAML. Folders are created and assigned in the application.

## 7. Deployment lifecycle

```
select -> resolve manifest -> preflight and diff -> approval (if required)
       -> take locks -> revalidate -> execute -> record, release, notify
```

Every stage is persisted. A run can be resumed from any of them.

### 7.1 Select

The user picks one environment, ticks groups or single services, and picks a version per group with optional per-service overrides.

- Version lists come from the ECR repository each service has set for that environment, filtered and ordered by the service's `versions` rule. A group's list is the tags every selected service has. Lists are cached in Valkey for 60 seconds.
- The orchestrator checks permission and freezes, then creates the run in `resolving`.

### 7.2 Resolve the manifest

A runner task, read-only towards the environment, pins everything the run will use:

- For each service: the ECS service ARN, the version tag and the **image digest** it points at, and the **task definition currently running** (kept as "previous").
- For each env file: repository, the branch chosen, the **commit** it was read at, the path, a checksum, and a copy of the content.
- For each TOML: source bucket and key, object version if the bucket has versioning, a checksum, and a copy of the content.
- For each config destination: a checksum and a copy of what is there now (kept as "previous").
- The commits of the inventory and job definition used.

Copies go to Yumdrake's artifact bucket, encrypted. The manifest's identity is a hash over all of it. Execution later copies from these stored copies, never from the sources again.

### 7.3 Preflight and diff

The same runner task then checks, without changing anything: that the deployment role can be assumed, each ECS service exists, each image digest exists, every config source was readable, and no freeze blocks the run. Permissions that cannot be proven without writing are reported as "not verified", not as passed.

The user sees a diff before starting, and an approver sees the same one:

- Per service: current version and task definition against the requested version.
- Per config file: unchanged, changed or new. For env files, the names of keys that were added, removed or changed. Values are never shown.
- Whether any env file came from the fallback branch instead of the version branch.
- Anything preflight could not verify.

A failed preflight ends the run as `failed` before anything was touched.

### 7.4 Approval

- Required only where the job's YAML asks for it. The YAML is protected by review in git.
- Bound to the manifest identity. Approving a run approves exactly that manifest.
- No locks are held while a run waits for approval.

### 7.5 Locks and revalidation

- When a run is ready to execute, the orchestrator takes locks on every ECS service ARN and every config destination URI in the manifest, in one transaction, all or nothing.
- If any is held, the run stays `queued` and its page names the run it is waiting for.
- With the locks held, the runner checks the targets again: each service still runs the task definition recorded as "previous", and each image digest still exists. If something changed, the run ends as `invalidated`, releases its locks, and offers "Plan again". A new plan needs a new approval.
- Locks are released when the run reaches a final state. A run in `needs_reconciliation` keeps them.

### 7.6 States

- Run: `resolving`, `waiting_approval`, `queued`, `running`, `cancelling`, then one of `succeeded`, `partially_succeeded`, `failed`, `cancelled`, `rejected`, `expired`, `invalidated`. `needs_reconciliation` is not final: it holds locks until resolved.
- Leg: `pending`, `queued`, `claimed`, `running`, `succeeded`, `failed`, `skipped`, `cancelled`, `unknown`.
- Result precedence: any `unknown` leg gives `needs_reconciliation`. Otherwise a cancelled run is `cancelled`. Otherwise all legs succeeded gives `succeeded`, some succeeded gives `partially_succeeded`, none gives `failed`.
- The CLI exits with zero only for `succeeded`.

### 7.7 Scheduling

- A leg is queued when its dependencies are met (section 6.3). A failed leg does not stop its siblings.
- A leg whose dependency failed or was skipped is `skipped`, and so is what depends on it.
- Concurrency is bounded: `max_parallel` per step, plus limits per environment, per AWS account and overall, set in Admin with defaults **(call)** of 10 per environment and 20 per account. Waiting legs are served oldest run first.
- The scheduler is a function of database state. Any orchestrator task may advance a run and takes a row lock on it while it does.
- Authorisation and freezes are checked again when a run moves from `queued` to `running`.

### 7.8 Queue, leases and recovery

- A runner claims a queued leg, receiving a lease and a generation number. It renews the lease while it works. Every write it makes is conditional on still holding that generation, so a stale runner cannot overwrite a newer attempt.
- A runner checks its lease before every external change and stops if it has lost it.
- Before each external change the runner records the intent, and afterwards the result. A leg whose lease expired is given to another runner, which first reconciles against the real state in AWS (section 8) and then continues from there.
- Transient AWS errors are retried with backoff and jitter. A leg that failed is never retried automatically.
- If the real state cannot be determined within the reconciliation deadline, the leg is `unknown`.
- On shutdown a runner stops claiming, finishes or checkpoints what it holds, and releases its leases.

### 7.9 Cancel

Cancelling is not rolling back.

| Leg is | What happens |
|---|---|
| Pending or queued | Cancelled without running |
| Copying config | Stops after the current file. The log lists what was written |
| Waiting on an ECS rollout it started | Keeps observing until ECS finishes, then records the real outcome |
| Finished | Left as it is |

The run is `cancelling` until every leg is settled, then `cancelled`. Locks are kept until then. The run page shows which services and files were changed.

### 7.10 Reconcile

A run in `needs_reconciliation` shows each unknown leg with what was intended and what AWS reports now. A person with the `restore` permission marks each as succeeded or failed after checking, or runs "Check again". The run then takes its final state and releases its locks. Every such decision is audited.

### 7.11 Logs

- Every line has: run, leg, sequence number, timestamp, level (`info`, `ok`, `warn`, `error`) and text. Run, leg and sequence together are unique, so a repeated batch is dropped.
- The runner commits lines to PostgreSQL in batches. A committed line is stored. An archiver moves finished legs to compressed objects in S3 and removes the rows once the object is confirmed.
- The orchestrator serves a log by merging archive and recent rows, filtered by step, service, level or text. A browser following a running log over SSE resumes with `Last-Event-ID`.
- A missing sequence range is shown as a gap, never hidden.
- Every AWS and GitHub call is logged with its outcome. Errors keep the full message and request id. Request bodies and config contents are never logged. Known secrets are redacted, on a best-effort basis.
- Each failed leg also stores a short reason and a full reason in PostgreSQL for the "What failed" panel, history tables and Slack.
- Reading logs needs the `view_logs` permission. Retention is a setting.

## 8. Built-in steps

Each step states how a second attempt recognises what a first attempt did.

### 8.1 `copy-config`

For each selected service, writes the manifest's stored copies to their destinations.

Branch choice, made at resolve time:

- The branch is `v` plus the version being deployed for that service, for example `v2.212.0`. A version that already starts with `v` is used as it is.
- If that branch does not exist and the environment is labelled `dev`, the branch `dev` is used instead. The diff and the log both say so.
- In `preprod` and `prod` there is no fallback: a missing version branch fails preflight, before anything is touched.
- A file missing from the branch that was chosen fails preflight. There is no per-file fallback.

Credentials: S3 reads and writes use the deployment role of the environment's account, so that role must be allowed to read the source bucket. Env files are read with the GitHub connection whose owner matches the first part of `source_env_path`.

Recovery: writes are idempotent. A second attempt compares each destination's checksum with the manifest and writes only what differs. A partial write is reported file by file.

### 8.2 `ecs-deploy`

For each selected service:

1. Register a new task definition revision identical to the manifest's "previous" one except that every container whose image is in the service's repository now points at the manifest's **image digest**. The revision is tagged with the version, run and leg.
2. Update the service to that revision.
3. Wait until ECS reports the rollout complete or failed, logging service events as they appear. Default timeout 15 minutes, settable with `timeout`.
4. Succeed only if the revision running at the end is the one this leg registered. If ECS rolled back by itself (deployment circuit breaker), the leg fails and says so.
5. On failure, log the stopped tasks' reasons and exit codes and record the reason.

Because the image is pinned by digest, the version shown on the Versions page is read from the revision's tags.

Recovery: a second attempt looks for a revision tagged with this run and leg. If one exists it is adopted instead of registering another. It then reads the service's deployments and resumes waiting. Updating a service to the revision it already uses is harmless.

### 8.3 Restore

Not a step in YAML but a run Yumdrake builds itself. "Restore" on a service or on a past run creates a run whose manifest is the recorded "previous" state: the exact previous task definition and the previous config file copies. It goes through the same permission (`restore`), approval, locking and execution as any run. Config copies are kept for 30 days, so a restore of an older run puts back the task definition only and says so.

A service's first-ever Yumdrake deployment can be restored too, because resolve records what was there before.

## 9. Sign-in, permissions and secrets

### 9.1 Sign-in

- **SAML** is the normal way in. Groups arrive in a SAML attribute and are mapped to Yumdrake groups in Admin. Membership is refreshed at each sign-in, and sessions have a maximum age so a removed user loses access.
- **Local accounts** exist for a break-glass admin and for service accounts. Passwords are stored as argon2id hashes. Use of the break-glass account is audited and announced.
- **Sessions** are server-side in Valkey behind a secure, http-only cookie, and can be revoked.
- **CLI tokens** belong to a user, carry that user's permissions and no more, are stored hashed, expire, and can be revoked. Only a browser session can create or revoke a token, so a token cannot renew itself (a token can only revoke itself); a global admin mints the token of a service account. A SAML user's token works only while that user's last SSO sign-in is at most `YUMDRAKE_TOKEN_SSO_MAX_AGE` old (default 168 hours), because group memberships refresh only at SSO sign-in; disabling a user revokes all their tokens.

### 9.2 Permissions

Actions: `view`, `view_logs`, `run`, `cancel`, `approve`, `restore`, `manage_jobs`, `manage_connections`, `manage_access`, `view_audit`.

A grant gives a group an action within a scope: an environment label, a single environment, and optionally a folder. A group may instead be a global admin. Every signed-in user has `view`.

`manage_access` does not confer admin power. Only a global admin may create a group with the admin flag, change a group's admin flag, add or remove members of an admin group, map an identity-provider group to an admin group (or change or remove such a mapping), disable or enable a user who is in an admin group, or change the single sign-on settings (whoever controls the identity provider can sign in as anyone). A `manage_access` holder who is not an admin gets 403 for these and can still manage non-admin groups and non-admin users, and read and test the single sign-on settings.

A non-admin who holds `manage_access` may create or delete a grant, add a user to a group or map an identity-provider group to a group only for an action and scope they hold themselves (a wider grant is refused), and grants of `manage_access`, `manage_connections` and `view_audit` can be created or deleted only by a global admin.

Every REST, SSE and CLI request is checked on the server. The UI hides or disables what a user may not do.

### 9.3 Audit

Every permission, freeze, connection, job and folder change, every run request, approval, rejection, cancellation, reconciliation, restore and lock release, with who and when.

### 9.4 Secrets

- Secrets Yumdrake must read back (credentials for AWS, GitHub and Slack, the SAML key) are encrypted with KMS before they are stored. The orchestrator can decrypt only what it uses; deployment credentials are decryptable only by the runner.
- Roles assumed with STS are preferred over access keys. Access keys remain possible.
- Manifest artifacts are encrypted, readable only by the runner, and deleted after 30 days. The UI never shows their contents.
- RDS storage is encrypted and connections require TLS.

## 10. Approvals, freezes, notifications

**Approvals**

- A job asks for approval only where its YAML says so. There are no admin-set policies in version 1.
- Who may approve is a permission. Nobody approves their own run unless they are a global admin.
- A waiting run expires after `approval.expires`, default 24 hours.
- Approve and reject happen in Yumdrake. Slack carries a link.

**Freezes**

- An admin can freeze an environment or a label with a reason and an expiry. No run may start executing there while it is frozen. Queued runs wait. Running runs continue.
- There is no bypass: lifting the freeze is the audited action.

**Notifications**

- For a job with `notify.slack`: the approval request, the decision, the start, and the result with what failed and why.
- Messages are queued in the database and retried. A Slack failure never changes a run's result.

## 11. Connections and sync

- **AWS accounts**, set in Admin. Each has a discovery credential for the orchestrator and a deployment credential for the runner, each a role to assume with an external id (preferred) or an access key.
- **GitHub**, set in Admin: a GitHub App (preferred) or a token, read-only. Several are allowed, one per owner; the owner in a repository path picks the connection.
- **Slack**, set in Admin: a bot token.
- **Inventory source**: one repository, branch and path.
- **Jobs** are added in the UI by pointing at a repository, branch and path.
- **Sync** is by polling, because the load balancer is internal and GitHub cannot reach it. Each source is checked every minute with conditional requests, and "Re-read" does it at once.
- The inventory is activated one commit at a time, as a whole. If any file at a commit is invalid, the whole previous inventory stays active and the error is shown. Jobs keep their last good version the same way.
- Every page that depends on YAML shows its commit and how long ago it was synced.

## 12. Web application

React with TypeScript, built with Vite, served by the orchestrator. Styling follows the mockup's tokens: IBM Plex Sans and Mono, neutral surfaces, colour reserved for status, status always shown with a shape and a label as well as colour. The **service strip** (one cell per service in a run) appears wherever a run is listed.

In the mockup today:

- **Overview**, **Jobs**, **Job** (History, Run, Definition), **Run**, **Runs**, **Approvals**, **Versions**, **Inventory**, **CLI**, **Admin** (users, roles, single sign-on, AWS accounts, GitHub, git sources, audit log).

To be added to the mockup before the web plans are written:

- **Plan and diff** on the job's Run tab and on the run page, replacing the current plain plan list.
- **Run page**: cancel, the new states, reconciliation, "Plan again", restore.
- **Queue**: active and queued runs, what each waits for, who holds which lock, runner capacity.
- **Service page**: current and previous versions per environment, deployment history, config revisions, restore.
- **Admin**: freezes, Slack, the two credentials per AWS account, concurrency limits.
- **System health**: runners, queue depth, expired leases, sync age, connection health.

## 13. API and CLI

- REST and JSON under `/api/v1`, plus SSE endpoints for a run's status and log. The web app and the CLI use the same API.
- CLI: `login`, `jobs list`, `plan` and `run` with `--env`, `--set` and `--only`, `runs watch`, `runs cancel`, `versions`, `approvals approve|reject`, `restore`, `validate`.
- `validate` checks inventory and job files locally with the same validator and schemas the orchestrator uses, so it can run in the config repository's CI.

## 14. Data

PostgreSQL holds:

- People and access: users, local credentials, groups, identity-provider group mappings, grants, CLI tokens, audit log.
- Governance: freezes.
- Connections and settings, with secrets encrypted.
- Definitions: folders, jobs, the active inventory snapshot with its commit.
- Runs: runs, manifests (the index; contents are in S3), steps, legs (also the work queue: status, lease, generation, intents, failure reasons), locks, approvals, recent log lines, the log archive index, the notification queue.

Valkey holds only what can be lost: sessions, version-list and Versions caches, wake-up messages.

S3 holds manifest artifacts and the log archive, with lifecycle rules for each.

## 15. Deployment and operations

Decided by the owner on 28 November 2026: Yumdrake must run in two ways with the same images.

- **Docker Compose on one host** is a supported way to run it for real. It needs no AWS service of its own: config copies and the log archive can live on volumes, encrypted by Yumdrake, and secrets are encrypted with a local key. A demo variant adds Floci and sample data so the whole system can be tried and tested end to end.
- **ECS**, for which the repository carries an example Terraform configuration. It is an example to adapt: validated with `terraform fmt` and `terraform validate`, never planned or applied from here, and it names no account, region, hostname or identity provider.

The points below describe the ECS setup the example shows.


- ECS on Fargate: `yumdrake-orchestrator` behind an internal HTTPS load balancer, `yumdrake-runner` with no inbound access, scaled on queue depth.
- RDS PostgreSQL with automated backups and point-in-time recovery, and a restore that has been tested. ElastiCache for Valkey. One S3 bucket. KMS keys. Database credentials in Secrets Manager.
- IAM: the orchestrator's role may assume only discovery roles. The runner's role may assume only deployment roles. `iam:PassRole` in a deployment role is limited to the task and execution roles of the services it deploys.
- Terraform lives under `deploy/terraform`, named in lowercase with hyphens.
- Images: multi-stage builds, pinned base tags, a `HEALTHCHECK`.
- Database migrations are backward compatible across one release, because the two services are deployed separately.
- Metrics to CloudWatch, with alarms: queue depth, age of the oldest queued leg, active runners, expired leases, run duration, failure rate, AWS throttles, sync age, log persistence failures, notification failures.

## 16. Testing

- Test first for behaviour: write the failing test, see it fail for the right reason, then implement.
- Unit: YAML validation and schemas; dependency rules for both scopes; scheduling, skip and result precedence; permission checks; branch choice and the dev-only fallback; task-definition rewrite.
- Integration against real PostgreSQL and Valkey: claiming, leases and generations; two schedulers on one run; atomic lock acquisition and the interleaving in D5; log batches, duplicates and gaps; SSE resume.
- Recovery: for each step, kill the runner immediately before and immediately after each external call, then check that the second attempt adopts or repeats correctly and that an undeterminable outcome becomes `unknown`.
- Cancellation in each leg state of the table in 7.9.
- Manifest: a tag, branch or S3 object changed after resolve does not change what is deployed; a changed target invalidates the run.
- AWS and GitHub sit behind interfaces with fakes that return real error shapes.
- End to end, the whole system runs against Floci, a local AWS emulator, for STS, ECR, ECS, S3 and KMS (`docs/research/2026-11-27-floci.md`). Floci never reports a failed rollout, so failure paths are driven by a stub ECS client in unit tests and by a response-rewriting proxy in front of Floci in end-to-end tests.
- Web: component tests for the plan, run and job pages, and a browser test of the main path: sign in, plan, run, watch a failure, open the failed service's log, restore.

## 17. Order of delivery

Each is its own implementation plan and ends with something that runs. Nothing that can change a real environment is reachable before plans 3 and 7 are in.

1. **Foundations**: repository layout, Go module, configuration, migrations, local environment (PostgreSQL, Valkey, fake AWS), CI, images.
2. **YAML and sync**: schemas, validator, GitHub connections, inventory and job sync, read API, `yumdrake validate`.
3. **Sign-in and permissions**: local accounts, SAML, groups and grants, CLI tokens, audit log.
4. **Run engine**: run and leg states, queue, leases and generations, scheduler with both dependency scopes, locks, cancellation, reconciliation, logs, SSE. Proven with fake steps.
5. **Manifest and preflight**: AWS connections with two credentials, version lists, resolve, preflight, diff, revalidation.
6. **Steps**: `copy-config` and `ecs-deploy`, each with its recovery contract. Restore.
7. **Approvals, freezes, Slack**.
8. **Web application, core**: shell, Jobs, Job with plan and diff, Run, Runs, Approvals, Inventory, Admin.
9. **Web application, visibility**: Overview, Versions, Queue, Service page, System health.
10. **CLI**, complete.
11. **Terraform and operations**: infrastructure, IAM, metrics and alarms, backups, first deployment.

## 18. Left to the builder

- Go libraries for routing, database access, migrations, SAML and Valkey. Each new dependency is weighed for maintenance, security and supply-chain risk before it is added.
- Exact table columns, indexes and API paths, within the shapes above.
- Lease length, poll intervals, batch sizes, reconciliation deadlines.
- How the version is recorded on a task definition revision, as long as the image is pinned by digest and the version stays readable.
- The React component breakdown and the query library.
- Whether charts are hand-built as in the mockup or use a small library.

## 19. Owner decisions and what is still open

Decided by the owner on 27 November 2026:

1. **Config fallback**: to the `dev` branch only in environments labelled `dev`. Blocked in preprod and prod.
2. **Environment policies**: none. Approval is decided only in each job's YAML. The review's concern, that editing the YAML can remove an approval, is accepted and covered by review in git.
3. **Config copies**: kept in Yumdrake's bucket, encrypted, for 30 days.
4. **Verification**: the ECS check inside `ecs-deploy` only. A separate verify step with HTTP and alarm checks comes after version 1.
5. **Script step**: after version 1.
6. **Scope**: restore, freezes, and the Queue and System health screens all stay in version 1.
7. **Name**: Yumdrake. The repository stays `yamdrake`.

Decided on 28 November 2026:

8. **Hosting details are not needed.** Terraform is delivered as an example with variables for everything site-specific.
9. **Docker Compose** is a supported way to run Yumdrake, alongside ECS.

Changes made while planning the implementation:

- A run has a `planned` state between `resolving` and starting: it plans itself, a person reads the plan and diff, and nothing proceeds until it is started (the CLI's `run --yes` starts it at once).
- The screens that were not in the mockup (plan and diff, reconciliation, freezes, queue, service page, system health) are specified in plans 8b and 9 and built from the application's own component kit, instead of being added to the mockup first.

## Appendix A. Review findings and what was done

| Finding | Done | Where |
|---|---|---|
| Lifecycle locking | Adopted | D5, 7.5 |
| Immutable manifests | Adopted. Resolve runs in the runner so the orchestrator never reads config contents | D4, 7.2 |
| Configuration restoration | Adopted. Resolve also records the previous config so a first deployment can be restored | 8.3 |
| Runner recovery and reconciliation | Adopted | D3, 7.8, 7.10, 8 |
| Separate IAM roles | Adopted. Access keys stay possible, as the owner asked for them | D8, 11, 15 |
| Preflight and diff | Adopted | 7.3 |
| Verification | The ECS check is part of `ecs-deploy`. The separate step is after version 1, by owner decision | 8.2, 19 |
| Concurrency control | Adopted | 7.7 |
| Cancellation | Adopted, reversing the earlier "not in version 1" | 7.9 |
| Dependency semantics | Adopted: both scopes, always explicit | 6.3 |
| Schema versioning | Adopted as `apiVersion` and `kind` at the top of each file, without a `metadata` and `spec` wrapper, to keep the files flat | 6 |
| Reusable workflows | Deferred, as the review suggests | 2 |
| Configurable version selection | Adopted | 6.2 |
| Environment protection policies | Not adopted, by owner decision. Approval stays per job | 10, 19 |
| Granular permissions | Adopted | 9.2 |
| `run` step hardening | The step is after version 1, by owner decision. The review's requirements apply when it is designed | D9, 19 |
| Secret handling | Adopted | 7.11, 9.4 |
| Deployment freeze | Adopted, without a bypass | 10 |
| Partial success and unknown outcomes | Adopted | 7.6 |
| Branch fallback | Adopted as a fixed rule: `dev` environments only | 8.1, 19 |
| Durable logging | Adopted with PostgreSQL as the acknowledged store | D6, 7.11 |
| UI additions | Adopted as mockup work before the web plans | 12 |
| Observability and operations | Adopted | 15 |
| Remove model probabilities from section 4 | Section 4 is now decision records. The scores are kept in Appendix B because the owner's working rules ask for them to be recorded | 4, B |

## Appendix B. Model scores

The owner's working rules ask for design decisions and review findings to be scored with the TypeSafe Jev model (`jev-1.14.0`) and the scores recorded. They are a second opinion from a model, not a measurement, and did not replace the reasoning in section 4.

First revision, probability given to the chosen option: PostgreSQL queue 0.86, one task per leg 0.82, S3 for durable logs 0.82, SSE 0.97, script step as a separate task 1.00.

Review findings, severity from 0 to 3 if left unaddressed, and probability that the finding is a real gap as opposed to scope expansion:

| Finding | Severity | Real gap |
|---|---|---|
| Protection policies | 2.98 | 0.89 |
| `run` step hardening | 2.93 | 0.88 |
| Immutable manifests | 2.84 | 0.90 |
| Verification | 2.80 | 0.88 |
| Deployment freeze | 2.76 | 0.81 |
| Branch fallback | 2.72 | 0.91 |
| Configuration restoration | 2.71 | 0.78 |
| Preflight and diff | 2.71 | 0.84 |
| Secret handling | 2.65 | 0.89 |
| Separate IAM roles | 2.64 | 0.89 |
| Lifecycle locking | 2.57 | 0.91 |
| Concurrency control | 2.33 | 0.81 |
| Granular permissions | 2.28 | 0.76 |
| Explicit dependencies | 2.12 | 0.69 |
| Partial success | 2.02 | 0.83 |
| Runner recovery | 2.02 | 0.90 |
| Cancellation | 2.00 | 0.74 |
| Configurable versions | 1.98 | 0.62 |
| Observability | 1.64 | 0.77 |
| Schema versioning | 1.56 | 0.71 |
| Durable logging | 1.42 | 0.75 |
| UI additions | 0.84 | 0.37 |
