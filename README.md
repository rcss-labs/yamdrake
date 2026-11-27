# Yumdrake

Yumdrake is a purpose-built deployment tool for ECS. It puts the right config files in place and rolls a new image version onto ECS services, a narrow job that general-purpose CI servers handle slowly.

DevOps describe jobs and inventory in YAML. QA, developers and DevOps run those jobs from a web page or a CLI and see what a run will change before it starts.

See the [design spec](docs/superpowers/specs/2026-11-27-yumdrake-design.md).

## Prerequisites

- Go 1.26
- Docker with Compose
- golangci-lint v2 (v2.15 is used in CI)

## Local services

```sh
make dev-up     # start PostgreSQL 17 and Valkey 8 and wait until healthy
make dev-down   # stop them and delete their volumes
```

PostgreSQL listens on `localhost:5432` (database, user and password `yumdrake`). Valkey listens on `localhost:6379`.

## Running the services locally

Build both binaries into `bin/`:

```sh
make build
```

Point both services at the local services:

```sh
export YUMDRAKE_DATABASE_URL='postgres://yumdrake:yumdrake@localhost:5432/yumdrake?sslmode=disable'
export YUMDRAKE_VALKEY_ADDR=localhost:6379
export YUMDRAKE_LOCAL_SECRET_KEY="$(openssl rand -base64 32)"   # required; keep it the same between runs

bin/yumdrake-orchestrator   # serves on :8080; applies migrations on start
bin/yumdrake-runner         # health endpoint on 127.0.0.1:8081; waits for the schema
```

- The orchestrator applies database migrations on start (`YUMDRAKE_MIGRATE_ON_START=true` by default).
- The runner never migrates. It waits until the schema is at the expected version.
- The orchestrator serves `/readyz` on port 8080, which reports status `ok` when the service is ready.
- Both services serve `GET /healthz` (liveness, always 200, runs no checks) and `GET /readyz` (readiness, 200 when the `postgres` and `valkey` checks pass, 503 otherwise): the orchestrator on `:8080`, the runner on `127.0.0.1:8081` by default.
- Both binaries accept subcommands: `version` prints the build version, and `healthcheck` runs a health check against the service's own address.

```sh
bin/yumdrake-orchestrator version
bin/yumdrake-orchestrator healthcheck
bin/yumdrake-runner healthcheck
```

## API

The orchestrator serves a JSON API under `/api/v1`. Until sign-in arrives, every route requires `Authorization: Bearer <YUMDRAKE_BOOTSTRAP_TOKEN>`. This bootstrap mechanism is temporary and goes away with sign-in. When `YUMDRAKE_BOOTSTRAP_TOKEN` is empty the API answers 404 and the rest of the orchestrator still runs.

Errors look like `{"error":{"code":"...","message":"..."}}` with codes `unauthorized`, `not_found`, `invalid`, `conflict` and `internal`. Request bodies are limited to 1 MB and unknown JSON fields are rejected. No response contains a token, a private key or ciphertext.

| Method and path | Does |
| --- | --- |
| `GET /api/v1/inventory` | Active commit and activation time, last check time, the sync error and its time, a newer rejected commit with its problems, environments, and services with the environments they exist in. |
| `GET /api/v1/inventory/services/{name}` | The full parsed service. |
| `PUT /api/v1/admin/inventory-source` | Set `repo`, `branch` and `path` of the inventory, then sync once. |
| `POST /api/v1/inventory/sync` | Sync the inventory now. |
| `GET /api/v1/folders`, `POST /api/v1/folders` | List folders; create one by `name`. |
| `GET /api/v1/jobs` | Every job source with its folder, commit, sync and check times, runnable flag, problem count and sync error. |
| `POST /api/v1/jobs` | Register a job file (`folder_id`, `repo`, `branch`, `path`), sync it once and return it. |
| `GET /api/v1/jobs/{id}` | The source, its last good `definition` and its current `problems`. |
| `PATCH /api/v1/jobs/{id}` | Move to another folder (`folder_id`). |
| `POST /api/v1/jobs/{id}/sync` | Sync that job now. |
| `GET /api/v1/admin/github-connections`, `POST /api/v1/admin/github-connections` | List connections without secrets; create one for an `owner` (stored in lower case) with `method` `token` (and `token`) or `app` (and `app_id`, `installation_id`, `private_key`). The secret is encrypted before it is stored. |

```sh
curl -H "Authorization: Bearer $YUMDRAKE_BOOTSTRAP_TOKEN" localhost:8080/api/v1/inventory
```

The orchestrator also polls GitHub every `YUMDRAKE_SYNC_INTERVAL` (inventory first, then each job source). A failure to reach GitHub is kept as `sync_error` next to the last good data, which stays in use.

## Web application

The web application lives in `web/` (React, TypeScript and Vite). It needs Node 24 and npm.

```sh
make web-install   # install dependencies from the lockfile
make web-lint      # ESLint and the TypeScript check
make web-test      # unit tests, run once
make web-build     # production build into web/dist
```

For development, run the orchestrator on port 8080 and start the dev server with `npm --prefix web run dev`. The dev server proxies `/api` and `/saml` to the orchestrator on `localhost:8080`.

## Testing

```sh
make lint               # gofmt, go vet and golangci-lint
make test               # unit tests with the race detector
make test-integration   # integration tests; needs `make dev-up` first
make smoke              # builds the images and starts the Compose "app" profile, then checks that it is healthy
```

`make smoke` needs Docker. It removes its containers and volumes on exit. See `scripts/smoke.sh -h` for its exit codes.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Clean shutdown. |
| `1` | Start-up or serve failure. |
| `2` | Configuration or usage error. |
| `130` | A second SIGINT or SIGTERM arrived during graceful shutdown and forced an immediate exit. |

## Environment variables

Every variable starts with `YUMDRAKE_`. Secrets are never logged; a database URL is only printed with its password replaced by `REDACTED`.

| Variable | Used by | Default | Rule |
| --- | --- | --- | --- |
| `YUMDRAKE_DATABASE_URL` | orchestrator, runner | none (required) | Must use the `postgres://` or `postgresql://` scheme and include a host. The query string must parse. |
| `YUMDRAKE_VALKEY_ADDR` | orchestrator, runner | none (required) | `host:port`, with a port from 1 to 65535. |
| `YUMDRAKE_LOG_LEVEL` | orchestrator, runner | `info` | One of `debug`, `info`, `warn`, `error`. |
| `YUMDRAKE_SHUTDOWN_TIMEOUT` | orchestrator, runner | `20s` | Go duration greater than zero. |
| `YUMDRAKE_STARTUP_TIMEOUT` | orchestrator, runner | `60s` | Go duration greater than zero. Bounds the database wait (both) and the schema wait (runner). |
| `YUMDRAKE_MIGRATE_TIMEOUT` | orchestrator | `10m` | Go duration greater than zero. Bounds the schema migration, separately from `YUMDRAKE_STARTUP_TIMEOUT`. |
| `YUMDRAKE_HTTP_ADDR` | orchestrator | `:8080` | `host:port`, with a port from 1 to 65535. Also read by `healthcheck`. |
| `YUMDRAKE_MIGRATE_ON_START` | orchestrator | `true` | Must be `true` or `false`. |
| `YUMDRAKE_HEALTH_ADDR` | runner | `127.0.0.1:8081` | `host:port`, with a port from 1 to 65535. Also read by `healthcheck`. |
| `YUMDRAKE_RUNNER_ID` | runner | the host name | Identifies the runner in its logs. If unset and the host name cannot be determined, startup fails. |
| `YUMDRAKE_SECRETS_MODE` | orchestrator, runner | `local` | Only `local` is supported in this release; any other value is a config error. |
| `YUMDRAKE_LOCAL_SECRET_KEY` | orchestrator, runner | none (required when the mode is `local`) | Base64 of exactly 32 bytes (for example `openssl rand -base64 32`). Encrypts stored GitHub credentials. For development and tests only; never printed. |
| `YUMDRAKE_BOOTSTRAP_TOKEN` | orchestrator | empty | If set, at least 32 characters. Guards `/api/v1`; empty disables the API. Temporary until sign-in. Never printed. |
| `YUMDRAKE_SYNC_INTERVAL` | orchestrator | `60s` | Go duration of at least `10s`. |
| `YUMDRAKE_GITHUB_API_URL` | orchestrator | `https://api.github.com` | An `http://` or `https://` URL. Overridden in tests. |
| `YUMDRAKE_TEST_DATABASE_URL` | integration tests | none (required with `-tags integration`) | Same format as `YUMDRAKE_DATABASE_URL`. Set by `make test-integration` and CI. |
| `YUMDRAKE_TEST_VALKEY_ADDR` | integration tests | none (required with `-tags integration`) | Same format as `YUMDRAKE_VALKEY_ADDR`. Set by `make test-integration` and CI. |

## Validating YAML

`make build` produces `bin/yumdrake`, the CLI that checks an inventory before it is synced:

```sh
bin/yumdrake validate <inventory-dir> [--job <file>]...
```

The inventory directory holds `environments.yaml` and one `services/*.yaml` file per service. Each `--job` file is parsed and checked against that inventory. Job files are cross-checked only when the inventory itself is valid.

- Each problem is printed to stderr as `file:line: message`, sorted by file then line.
- On success stdout shows `OK: 5 environments, 2 services, 1 jobs`. On failure it shows `FAILED: N problems`.

Exit codes: `0` valid, `1` problems found or the directory is missing, `2` usage error (no directory, an unknown command, or bad flags).

```sh
make build
bin/yumdrake validate internal/spec/testdata/valid --job internal/spec/testdata/valid/jobs/deploy-backend.yaml
```

The JSON Schemas in `schemas/` (`environments.schema.json`, `service.schema.json`, `job.schema.json`) exist for editor completion only. The Go validator is the authority: a file the schema accepts can still fail `validate`. To get completion in an editor with the YAML language server, put a modeline at the top of a file:

```yaml
# yaml-language-server: $schema=https://yumdrake.dev/schemas/service.schema.json
apiVersion: yumdrake/v1
kind: Service
```

## CI

The GitHub Actions workflow (`.github/workflows/ci.yml`) runs lint, test, integration, smoke and web jobs on pull requests and on pushes to `main`.
