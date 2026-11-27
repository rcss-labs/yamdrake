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

## Testing

```sh
make lint               # gofmt, go vet and golangci-lint
make test               # unit tests with the race detector
make test-integration   # integration tests; needs `make dev-up` first
make smoke              # builds the images and starts the Compose "app" profile, then checks that it is healthy
```

`make smoke` needs Docker. It removes its containers and volumes on exit. See `scripts/smoke.sh -h` for its exit codes.

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
| `YUMDRAKE_TEST_DATABASE_URL` | integration tests | none (required with `-tags integration`) | Same format as `YUMDRAKE_DATABASE_URL`. Set by `make test-integration` and CI. |
| `YUMDRAKE_TEST_VALKEY_ADDR` | integration tests | none (required with `-tags integration`) | Same format as `YUMDRAKE_VALKEY_ADDR`. Set by `make test-integration` and CI. |

## CI

The GitHub Actions workflow (`.github/workflows/ci.yml`) runs lint, test, integration and smoke jobs on pull requests and on pushes to `main`.
