# Yumdrake Plan 1: Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two Go services, `yumdrake-orchestrator` and `yumdrake-runner`, that start, connect to PostgreSQL and Valkey, apply migrations safely, report health, shut down gracefully, and are built, tested and packaged by one command locally and by CI.

**Architecture:** One Go module with a `cmd/` entry point per service and shared packages under `internal/`. The orchestrator applies embedded SQL migrations on start under a database lock; the runner waits until the schema is at the version it was built with. Both expose liveness and readiness over HTTP and exit cleanly on SIGTERM.

**Tech Stack:** Go 1.26, standard library `net/http` and `log/slog`, `github.com/jackc/pgx/v5` (hand-written SQL, no code generation), `github.com/pressly/goose/v3` (embedded migrations with its PostgreSQL session locker), `github.com/valkey-io/valkey-go`, PostgreSQL 17, Valkey 8, Docker with distroless runtime images, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 5, 14, 15, 16 and 17 item 1)

This plan follows the owner's rule for plans: no full code. Each task gives files, signatures, behaviour, test names with their cases, and acceptance criteria. The implementer writes the bodies and the test code.

## Global Constraints

- Module path: `github.com/rcss-labs/yamdrake`. Product and binary names use `yumdrake`: `yumdrake-orchestrator`, `yumdrake-runner`.
- Go version in `go.mod`: `go 1.26`.
- Only the three third-party modules named in Tech Stack. Adding any other needs a recorded evaluation of maintenance, security and supply-chain risk and an escalation, not a quiet `go get`.
- Tests use the standard `testing` package only. No assertion library.
- Test first: write the test, run it, see it fail for the reason stated, then implement.
- Integration tests are behind the build tag `integration` and read `YUMDRAKE_TEST_DATABASE_URL` and `YUMDRAKE_TEST_VALKEY_ADDR`. They fail, not skip, when the tag is set and a variable is missing.
- Secrets are never logged or returned in HTTP bodies. A database URL is only ever printed through `config.RedactURL`.
- Logs are structured JSON on stdout through `log/slog`.
- Every environment variable starts with `YUMDRAKE_`.
- Docker: multi-stage builds, every image pinned to a specific tag or digest (never `latest`), a `HEALTHCHECK` in each image, non-root user.
- Shell scripts start with `set -euo pipefail`, print usage for `-h`, and exit with meaningful codes.
- Conventional commits (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`), with a body that says why. Work on a branch, not `main`. Never commit secrets.
- Run `make lint test` before every commit. Report what was run and its result; never claim a check that was not run.
- Not in this plan (they arrive with the plan that first needs them): a fake AWS for local use, the React app, the CLI, any table beyond `instance_settings`, sign-in.

## Library decisions

Scored with TypeSafe Jev (`jev-1.14.0`) for fit, maintenance, security and supply-chain surface; probability of the chosen option in brackets.

- Routing: standard library `net/http` (0.98). No dependency; Go 1.22 patterns cover method and path matching.
- Database: `pgx/v5` with hand-written SQL and manual scanning (0.56, against 0.43 for adding sqlc). The queue needs `FOR UPDATE SKIP LOCKED` and conditional updates that are clearest as plain SQL, and it avoids a code-generation tool.
- Migrations: `goose/v3` as a library (0.61, against 0.35 for golang-migrate). Embedded files and a built-in PostgreSQL session locker.
- Valkey: `valkey-io/valkey-go`, the official client (0.75).
- Integration tests: real servers from Docker Compose and CI service containers, addressed by environment variables (0.86). No test-only Go dependency.
- Runtime image: `gcr.io/distroless/static`, non-root, pinned by digest (0.79). No shell, so the health check is the binary's own subcommand.

## File Structure

| Path | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Module and dependencies |
| `Makefile` | The one way to build, lint, test, start local services |
| `.golangci.yml` | Lint rules |
| `compose.yaml` | Local PostgreSQL and Valkey; the two services under the `app` profile |
| `internal/version/version.go` | Build version and commit, set at link time |
| `internal/config/config.go` | Read and validate environment variables; redact URLs |
| `internal/logging/logging.go` | Build the JSON logger |
| `internal/db/db.go` | Open the pool, wait for the database, ping |
| `internal/db/migrate.go` | Apply embedded migrations under a lock; report versions |
| `internal/db/migrations/00001_instance_settings.sql` | First migration |
| `internal/cache/cache.go` | Open the Valkey client, ping |
| `internal/health/health.go` | `/healthz`, `/readyz`, and the probe used by `HEALTHCHECK` |
| `internal/app/app.go` | Run HTTP servers until the context ends, then shut down gracefully |
| `cmd/orchestrator/main.go` | Orchestrator entry point |
| `cmd/runner/main.go` | Runner entry point |
| `deploy/docker/orchestrator.Dockerfile`, `deploy/docker/runner.Dockerfile` | Images |
| `scripts/smoke.sh` | Start the stack in Compose and check both services become healthy |
| `.github/workflows/ci.yml` | Lint, unit tests, integration tests, image build, smoke |
| `README.md` | How to run and test locally |

Each `internal` package has its tests beside it (`*_test.go`; integration tests in `*_integration_test.go` with the `integration` tag).

## Review Focus

Conditions the spec implies that an operator will meet. Each is pinned by a test in the task named.

1. A required environment variable is missing or malformed: the service exits with one message naming every bad variable, never a panic or a stack trace. (Task 2)
2. The database is not reachable yet when a service starts, as happens on every ECS deploy: the service keeps trying for a bounded time, then exits non-zero with a clear message. (Task 3)
3. Two orchestrator tasks start at the same moment: migrations are applied exactly once and both start. (Task 3)
4. Valkey goes away while the service runs: liveness stays healthy, readiness reports unavailable and names Valkey, and the process does not exit. (Tasks 5 and 6)
5. A failing dependency's error text contains a password: it never reaches a log line or the readiness response. (Tasks 2 and 5)

---

### Task 1: Module, tooling and version

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `internal/version/version.go`
- Modify: `.gitignore` (add `/bin/`, `/coverage.out`)
- Test: `internal/version/version_test.go`

**Interfaces:**
- Produces: `version.Version`, `version.Commit` (package-level `string` variables, defaults `"dev"` and `"unknown"`, set with `-ldflags -X`); `func version.String() string` returning `"<version> (<commit>)"`.
- Produces Make targets used by every later task: `build` (both binaries into `bin/`, with ldflags from `git describe --tags --always --dirty` and `git rev-parse --short HEAD`), `lint` (`gofmt -l` must print nothing, `go vet ./...`, `golangci-lint run`), `test` (`go test -race ./...`), `dev-up`, `dev-down`, `test-integration` (the last three are filled in by Task 3).

- [ ] **Step 1: Write the failing test** `TestStringIncludesVersionAndCommit`: with `Version="1.2.3"` and `Commit="abc1234"` set in the test, `String()` returns `"1.2.3 (abc1234)"`; with the defaults it returns `"dev (unknown)"`.
- [ ] **Step 2: Run** `go test ./internal/version/` and confirm it fails because the package does not exist.
- [ ] **Step 3: Create** `go.mod` (`go mod init github.com/rcss-labs/yamdrake`, `go 1.26`) and `internal/version/version.go`.
- [ ] **Step 4: Create** `Makefile` with the targets above and a `help` target that lists them. `golangci-lint` is expected on the PATH; `lint` prints how to install it if it is missing and exits 2.
- [ ] **Step 5: Create** `.golangci.yml` enabling `errcheck`, `govet`, `staticcheck`, `ineffassign`, `unused`, `gosec`, `bodyclose`, `errorlint`, `misspell`.
- [ ] **Step 6: Run** `make lint test`. Expected: both pass, `version` test green.
- [ ] **Step 7: Commit** `chore: add Go module, Makefile and lint configuration`.

**Acceptance:** `make help`, `make lint` and `make test` succeed on a clean checkout with Go 1.26 and golangci-lint installed.

---

### Task 2: Configuration and logging

**Files:**
- Create: `internal/config/config.go`, `internal/logging/logging.go`
- Test: `internal/config/config_test.go`, `internal/logging/logging_test.go`

**Interfaces:**
- Produces:

```go
type Common struct {
    DatabaseURL     string
    ValkeyAddr      string
    LogLevel        slog.Level
    ShutdownTimeout time.Duration
    StartupTimeout  time.Duration
}
type Orchestrator struct {
    Common
    HTTPAddr       string
    MigrateOnStart bool
}
type Runner struct {
    Common
    HealthAddr string
    RunnerID   string
}
func LoadOrchestrator(getenv func(string) string) (Orchestrator, error)
func LoadRunner(getenv func(string) string) (Runner, error)
func RedactURL(raw string) string
func logging.New(w io.Writer, level slog.Level, service string) *slog.Logger
```

Variables and defaults:

| Variable | Used by | Default | Rule |
|---|---|---|---|
| `YUMDRAKE_DATABASE_URL` | both | none, required | must parse as a `postgres://` or `postgresql://` URL |
| `YUMDRAKE_VALKEY_ADDR` | both | none, required | `host:port` |
| `YUMDRAKE_LOG_LEVEL` | both | `info` | `debug`, `info`, `warn` or `error` |
| `YUMDRAKE_SHUTDOWN_TIMEOUT` | both | `20s` | Go duration, greater than zero |
| `YUMDRAKE_STARTUP_TIMEOUT` | both | `60s` | Go duration, greater than zero |
| `YUMDRAKE_HTTP_ADDR` | orchestrator | `:8080` | `host:port` |
| `YUMDRAKE_MIGRATE_ON_START` | orchestrator | `true` | `true` or `false` |
| `YUMDRAKE_HEALTH_ADDR` | runner | `127.0.0.1:8081` | `host:port` |
| `YUMDRAKE_RUNNER_ID` | runner | the host name | non-empty |

Behaviour:
- A load function collects every problem and returns them joined in one error, each line `config: <VARIABLE>: <what is wrong>`. It never includes a variable's value for `YUMDRAKE_DATABASE_URL`.
- `RedactURL` replaces the password in a URL with `REDACTED` and returns `"invalid URL"` for input it cannot parse, so a bad value is never echoed.
- `logging.New` writes JSON lines with the keys `time`, `level`, `msg`, `service`, `version` (from `version.String()`).

- [ ] **Step 1: Write the failing tests** in `config_test.go`:
  - `TestLoadOrchestratorDefaults`: only the two required variables set; every other field has the default in the table.
  - `TestLoadOrchestratorOverrides`: every variable set; every field reflects it.
  - `TestLoadReportsEveryProblemAtOnce`: database URL missing, log level `loud`, shutdown timeout `-1s`; the error mentions all three variable names.
  - `TestLoadErrorNeverContainsDatabasePassword`: database URL `postgres://u:s3cret@host/db?sslmode=bogus%zz` (unparseable); the error text does not contain `s3cret`.
  - `TestLoadRunnerDefaultsRunnerIDToHostname`: `RunnerID` equals `os.Hostname()`.
  - `TestRedactURL`: table of `postgres://u:s3cret@h:5432/db?sslmode=require` to the same URL with `REDACTED`; a URL with no password unchanged; `"::not a url"` to `"invalid URL"`.
- [ ] **Step 2: Run** `go test ./internal/config/` and confirm it fails because the package does not exist.
- [ ] **Step 3: Implement** `internal/config/config.go`. Parse with the standard library only.
- [ ] **Step 4: Write the failing test** `TestNewWritesJSONWithServiceAndVersion` in `logging_test.go`: log one info line to a buffer; the decoded JSON has `service` equal to the argument, a non-empty `version`, and `msg` equal to what was logged. `TestNewHonoursLevel`: a debug line is absent at level info.
- [ ] **Step 5: Implement** `internal/logging/logging.go`.
- [ ] **Step 6: Run** `make lint test`. Expected: pass.
- [ ] **Step 7: Commit** `feat: add configuration loading and JSON logging`.

**Acceptance:** all eight tests pass; no test or code path prints a database password.

---

### Task 3: Database pool, migrations and local services

**Files:**
- Create: `compose.yaml`, `internal/db/db.go`, `internal/db/migrate.go`, `internal/db/migrations/00001_instance_settings.sql`
- Modify: `Makefile` (fill in `dev-up`, `dev-down`, `test-integration`), `go.mod`
- Test: `internal/db/db_test.go`, `internal/db/db_integration_test.go`

**Interfaces:**
- Consumes: `config.RedactURL`.
- Produces:

```go
func Open(ctx context.Context, url string) (*pgxpool.Pool, error)
func WaitOpen(ctx context.Context, url string, maxWait time.Duration, log *slog.Logger) (*pgxpool.Pool, error)
func Migrate(ctx context.Context, url string) error
func SchemaVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error)
func LatestVersion() int64
func WaitSchema(ctx context.Context, pool *pgxpool.Pool, maxWait time.Duration) error
```

Behaviour:
- `Open` creates the pool and pings once. Any error it returns has the URL passed through `config.RedactURL`.
- `WaitOpen` retries `Open` with backoff starting at 500ms and capped at 5s until it succeeds or `maxWait` passes. It logs each failed attempt at warn level with the redacted URL. On giving up it returns an error that says how long it waited.
- `Migrate` applies the SQL files embedded from `internal/db/migrations` with goose, using goose's PostgreSQL session locker so that concurrent callers apply each migration once. It is a no-op when the schema is current.
- `SchemaVersion` returns the highest applied migration version, or 0 on a database goose has never touched.
- `LatestVersion` returns the highest version among the embedded files.
- `WaitSchema` polls `SchemaVersion` every second until it is at least `LatestVersion()`, or returns an error naming both versions after `maxWait`.

Migration `00001_instance_settings.sql` (goose up and down sections): table `instance_settings` with `key text primary key`, `value jsonb not null`, `updated_at timestamptz not null default now()`. It will hold application settings such as the inventory source (spec section 14).

`compose.yaml` services, images pinned to an exact patch tag that exists at the time of writing (check with `docker manifest inspect`):
- `postgres`: PostgreSQL 17, database, user and password all `yumdrake`, host port 5432, a `pg_isready` health check.
- `valkey`: Valkey 8, host port 6379, a `valkey-cli ping` health check.

Make targets: `dev-up` runs `docker compose up -d --wait postgres valkey`; `dev-down` runs `docker compose down -v`; `test-integration` exports `YUMDRAKE_TEST_DATABASE_URL=postgres://yumdrake:yumdrake@localhost:5432/yumdrake?sslmode=disable` and `YUMDRAKE_TEST_VALKEY_ADDR=localhost:6379` and runs `go test -race -tags integration ./...`.

- [ ] **Step 1: Write the failing unit test** `TestLatestVersionMatchesEmbeddedFiles` in `db_test.go`: `LatestVersion()` equals 1.
- [ ] **Step 2: Run** `go test ./internal/db/` and confirm it fails because the package does not exist.
- [ ] **Step 3: Create** `compose.yaml` and the Make targets. Run `make dev-up` and confirm both containers report healthy.
- [ ] **Step 4: Write the failing integration tests** in `db_integration_test.go`. Each test creates its own schema-clean database (a helper `newTestDatabase(t)` that creates a uniquely named database from the test URL and drops it on cleanup) so tests do not affect each other.
  - `TestMigrateAppliesAllAndIsIdempotent`: after `Migrate`, `SchemaVersion` equals `LatestVersion()` and `instance_settings` exists; a second `Migrate` returns nil and the version is unchanged.
  - `TestMigrateConcurrentCallersApplyOnce`: eight goroutines call `Migrate` on the same fresh database at once; all return nil and the goose version table has exactly one row per migration. (Review Focus 3)
  - `TestSchemaVersionIsZeroOnFreshDatabase`.
  - `TestWaitSchemaTimesOutNamingVersions`: on a fresh, unmigrated database with `maxWait` 2s, the error mentions `0` and `1`.
  - `TestWaitOpenGivesUpWithRedactedURL`: URL `postgres://yumdrake:s3cret@127.0.0.1:1/none`, `maxWait` 2s; the call returns within 4s, the error mentions the wait, and neither the error nor the captured log output contains `s3cret`. (Review Focus 2 and 5)
  - `TestWaitOpenSucceedsWhenDatabaseAppearsLate`: start the wait against a port with nothing listening, start a TCP proxy to the real database on that port after 1s, and expect success. Use a small in-test proxy with `net.Listen`; no extra dependency.
- [ ] **Step 5: Run** `make test-integration` and confirm these fail because the functions do not exist.
- [ ] **Step 6: Add** `pgx/v5` and `goose/v3` with `go get`, pinned to their latest released versions, and implement `db.go`, `migrate.go` and the migration file.
- [ ] **Step 7: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 8: Commit** `feat: add database pool, locked migrations and local services`.

**Acceptance:** the seven tests pass against the Compose PostgreSQL; `make dev-up` and `make dev-down` work from a clean machine with Docker.

---

### Task 4: Valkey client

**Files:**
- Create: `internal/cache/cache.go`
- Modify: `go.mod`
- Test: `internal/cache/cache_integration_test.go`

**Interfaces:**
- Produces:

```go
func Open(addr string) (valkey.Client, error)
func Ping(ctx context.Context, c valkey.Client) error
```

Behaviour:
- `Open` builds a client for `addr`. It does not fail when Valkey is unreachable; the cache is allowed to be absent at start. It fails only for an address it cannot use.
- `Ping` sends `PING` and returns nil on `PONG`, an error otherwise, within the context's deadline.

- [ ] **Step 1: Write the failing integration tests**:
  - `TestPingSucceedsAgainstRunningValkey`.
  - `TestOpenDoesNotFailWhenValkeyIsDown`: address `127.0.0.1:1`; `Open` returns a client and nil.
  - `TestPingFailsWithinDeadlineWhenValkeyIsDown`: same client, context with a 1s deadline; `Ping` returns an error in under 2s.
- [ ] **Step 2: Run** `make test-integration` and confirm they fail because the package does not exist.
- [ ] **Step 3: Add** `valkey-io/valkey-go` at its latest released version and implement `cache.go`. If the client's constructor insists on a reachable server, configure it not to (the library has an option for this) so that `Open` meets the behaviour above.
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add Valkey client that tolerates an absent cache`.

**Acceptance:** the three tests pass; a service can be constructed with Valkey down.

---

### Task 5: Health endpoints and probe

**Files:**
- Create: `internal/health/health.go`
- Test: `internal/health/health_test.go`

**Interfaces:**
- Produces:

```go
type Check struct {
    Name string
    Fn   func(context.Context) error
}
func Handler(checks ...Check) http.Handler
func Probe(ctx context.Context, url string) error
```

Behaviour:
- `Handler` serves `GET /healthz` and `GET /readyz`. Other paths are 404, other methods 405.
- `/healthz` always returns 200 with body `{"status":"ok"}`. It runs no checks.
- `/readyz` runs every check concurrently, each with a 2s timeout. All nil: 200 and `{"status":"ok","checks":{"<name>":"ok",...}}`. Any error: 503 and `"status":"unavailable"`, with the failing check's value `"failed"`. The error's own text is not put in the response; the caller logs it.
- `Handler` takes an optional logger through a functional option `WithLogger(*slog.Logger)`; when a check fails it logs the check name and the error at warn level.
- `Probe` does a GET with the context's deadline and returns nil only for status 200.

- [ ] **Step 1: Write the failing tests** using `httptest`:
  - `TestHealthzIsOKWithoutRunningChecks`: a check that would fail is registered; `/healthz` is 200 and the check function was not called.
  - `TestReadyzOKWhenAllChecksPass`: two passing checks; 200; both names map to `"ok"`.
  - `TestReadyzUnavailableNamesFailingCheck`: `postgres` passes, `valkey` returns an error; 503; `checks.valkey` is `"failed"`, `checks.postgres` is `"ok"`. (Review Focus 4)
  - `TestReadyzDoesNotLeakErrorText`: the failing check's error is `dial postgres://u:s3cret@h/db: refused`; the response body does not contain `s3cret` or `refused`. (Review Focus 5)
  - `TestReadyzTimesOutSlowCheck`: a check that blocks until its context ends; the response arrives in under 3s with 503.
  - `TestUnknownPathIs404AndPostIs405`.
  - `TestProbe`: nil against a server returning 200; error against one returning 503; error against a closed port.
- [ ] **Step 2: Run** `go test ./internal/health/` and confirm it fails because the package does not exist.
- [ ] **Step 3: Implement** `internal/health/health.go` with the standard library.
- [ ] **Step 4: Run** `make lint test`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add liveness, readiness and health probe`.

**Acceptance:** the seven tests pass.

---

### Task 6: Service lifecycle and the two binaries

**Files:**
- Create: `internal/app/app.go`, `cmd/orchestrator/main.go`, `cmd/runner/main.go`
- Test: `internal/app/app_test.go`, `cmd/orchestrator/main_integration_test.go`, `cmd/runner/main_integration_test.go`

**Interfaces:**
- Consumes: everything produced by Tasks 1 to 5.
- Produces:

```go
// internal/app
func Serve(ctx context.Context, log *slog.Logger, shutdownTimeout time.Duration, servers ...*http.Server) error

// cmd/orchestrator and cmd/runner, each
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int
```

Behaviour of `app.Serve`:
- Starts every server. Returns when the context is cancelled or any server fails to listen.
- On cancellation it calls `Shutdown` on every server with `shutdownTimeout`, so requests in flight finish. It returns nil for a clean shutdown and an error if a server could not listen or did not stop in time.

Behaviour of both `run` functions:
- `args[1]` selects a subcommand: none means serve; `version` prints `version.String()` and returns 0; `healthcheck` calls `health.Probe` against the service's own `/healthz` on loopback (port taken from the configured address) with a 3s deadline and returns 0 or 1. Anything else prints usage to stderr and returns 2.
- Serving: load config (on error print it to stderr, return 2); build the logger; log one `starting` line with the redacted database URL; open the database with `db.WaitOpen` bounded by `StartupTimeout` (on failure log and return 1); open the cache; serve health; on SIGINT or SIGTERM shut down and return 0.
- `main` only calls `run` with `signal.NotifyContext` for SIGINT and SIGTERM, `os.Args`, `os.Getenv`, and exits with the result.

Orchestrator specifics: if `MigrateOnStart`, call `db.Migrate` before serving (on failure log and return 1). Serve `health.Handler` on `HTTPAddr` with checks `postgres` and `valkey`.

Runner specifics: never migrates. After opening the database, call `db.WaitSchema` bounded by `StartupTimeout` (on failure log and return 1). Serve `health.Handler` on `HealthAddr` with the same two checks. Log its `RunnerID` at start. It claims no work yet.

- [ ] **Step 1: Write the failing tests** in `app_test.go`:
  - `TestServeReturnsNilOnCancel`: one server on `127.0.0.1:0`; cancel the context; `Serve` returns nil within the timeout.
  - `TestServeLetsInFlightRequestFinish`: a handler that sleeps 300ms; start a request, cancel the context after 50ms; the client still receives 200 and `Serve` returns nil.
  - `TestServeReportsListenError`: two servers on the same fixed port; `Serve` returns an error and does not hang.
  - `TestServeErrorsWhenShutdownExceedsTimeout`: a handler that blocks for 2s, shutdown timeout 100ms; `Serve` returns an error within about 100ms.
- [ ] **Step 2: Run** `go test ./internal/app/` and confirm it fails because the package does not exist.
- [ ] **Step 3: Implement** `internal/app/app.go`.
- [ ] **Step 4: Write the failing integration tests** for the orchestrator (each on a fresh test database and a free port, calling `run` in a goroutine with an injected `getenv`):
  - `TestRunVersionPrintsVersion`: returns 0, stdout contains `version.String()`.
  - `TestRunUnknownSubcommandReturns2`.
  - `TestRunWithBadConfigReturns2AndNamesVariable`: no database URL; stderr names `YUMDRAKE_DATABASE_URL`; nothing panics. (Review Focus 1)
  - `TestRunMigratesThenBecomesReady`: `/readyz` reaches 200 within 10s; the schema version equals `db.LatestVersion()`; cancelling the context makes `run` return 0.
  - `TestRunStaysLiveButNotReadyWhenValkeyIsDown`: Valkey address `127.0.0.1:1`; `/healthz` is 200, `/readyz` is 503 with `checks.valkey` `"failed"`, and `run` has not returned after 3s. (Review Focus 4)
  - `TestRunExits1WhenDatabaseNeverAppears`: database on `127.0.0.1:1`, `YUMDRAKE_STARTUP_TIMEOUT=2s`; `run` returns 1 and stdout does not contain the password from the URL. (Review Focus 2 and 5)
  - `TestHealthcheckSubcommand`: returns 0 while a serving instance is up on the same address, 1 when nothing is listening.
- [ ] **Step 5: Write the failing integration tests** for the runner:
  - `TestRunnerWaitsForSchemaThenBecomesReady`: start the runner on an unmigrated database, call `db.Migrate` after 1s; `/readyz` reaches 200.
  - `TestRunnerExits1WhenSchemaNeverArrivesAndDoesNotMigrate`: unmigrated database, startup timeout 2s; returns 1, the log names both versions, and `SchemaVersion` is still 0 afterwards.

  Add to `internal/config/config_test.go`: `TestLoadRunnerDefaultsHealthAddrToLoopback`, asserting `HealthAddr` is `127.0.0.1:8081`, so the runner is not reachable from outside its task by default.
- [ ] **Step 6: Run** `make test-integration` and confirm these fail because the commands do not exist.
- [ ] **Step 7: Implement** both `main.go` files. Keep the shared start-up sequence in one unexported helper inside `internal/app` if the two would otherwise repeat it.
- [ ] **Step 8: Run** `make lint build test test-integration`. Expected: pass, and `bin/yumdrake-orchestrator version` prints a version.
- [ ] **Step 9: Commit** `feat: add orchestrator and runner entry points with graceful lifecycle`.

**Acceptance:** all fourteen tests pass; both binaries start against `make dev-up`, answer `/healthz` and `/readyz`, and exit 0 on Ctrl-C.

---

### Task 7: Images and smoke test

**Files:**
- Create: `deploy/docker/orchestrator.Dockerfile`, `deploy/docker/runner.Dockerfile`, `.dockerignore`, `scripts/smoke.sh`
- Modify: `compose.yaml` (add `orchestrator` and `runner` under profile `app`), `Makefile` (add `images`, `smoke`)

**Interfaces:**
- Consumes: `make build` ldflags, the `healthcheck` subcommand.
- Produces: images `yumdrake-orchestrator:dev` and `yumdrake-runner:dev`; `make images`; `make smoke`.

Dockerfile requirements, the same shape for both:
- Build stage on the official Go 1.26 image, pinned to an exact patch tag. `CGO_ENABLED=0`, `-trimpath`, the version ldflags passed as build arguments `VERSION` and `COMMIT`. Dependencies downloaded in a layer of their own before the source is copied.
- Final stage `gcr.io/distroless/static-debian12` pinned by digest, with the tag it was resolved from in a comment. User `nonroot`.
- `EXPOSE 8080` for the orchestrator only.
- `HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 CMD ["/yumdrake-orchestrator","healthcheck"]` (and the runner's equivalent).
- `ENTRYPOINT` is the binary.

Compose additions: both services build from their Dockerfile, depend on `postgres` and `valkey` being healthy, and get `YUMDRAKE_DATABASE_URL` and `YUMDRAKE_VALKEY_ADDR` pointing at the Compose service names. The orchestrator publishes 8080.

`scripts/smoke.sh`: usage text for `-h`; builds and starts the `app` profile with `docker compose --profile app up -d --build --wait --wait-timeout 120`; then checks `curl -fsS http://localhost:8080/readyz` returns status `ok`, and that `docker inspect` reports both service containers `healthy`; always runs `docker compose --profile app down -v` on exit through a trap. Exit codes: 0 pass, 1 a check failed, 2 usage, 3 Docker is not available.

- [ ] **Step 1: Write** `scripts/smoke.sh` and run it. Expected: exit 1 or a Compose error, because the images and Compose services do not exist yet.
- [ ] **Step 2: Create** the two Dockerfiles and `.dockerignore` (exclude `.git`, `bin`, `docs`, `.idea`).
- [ ] **Step 3: Add** the Compose services and the `images` and `smoke` Make targets.
- [ ] **Step 4: Run** `make smoke`. Expected: exit 0, with both containers healthy.
- [ ] **Step 5: Confirm** the images run as non-root and contain no shell: `docker run --rm --entrypoint sh yumdrake-orchestrator:dev` fails to start, and `docker inspect -f '{{.Config.User}}' yumdrake-orchestrator:dev` is not empty or `root`.
- [ ] **Step 6: Run** `bash -n scripts/smoke.sh` and, if `shellcheck` is installed, `shellcheck scripts/smoke.sh`. Expected: clean.
- [ ] **Step 7: Commit** `feat: add container images and a Compose smoke test`.

**Acceptance:** `make smoke` passes from a clean checkout. Automated unit testing of Dockerfiles is impractical; the smoke script is the validation.

---

### Task 8: Continuous integration and README

**Files:**
- Create: `.github/workflows/ci.yml`, `README.md`

**Interfaces:**
- Consumes: Make targets `lint`, `test`, `test-integration`, `smoke`.

Workflow requirements:
- Triggers: pull requests, and pushes to `main`.
- `permissions: contents: read` at the top level. No secrets are used.
- Every third-party action is pinned to a full commit SHA with its version in a comment. Resolve the SHAs when writing the file, for example with `gh api repos/actions/checkout/git/ref/tags/<tag>`.
- Job `lint`: set up Go from `go.mod`, install golangci-lint at a pinned version, `make lint`.
- Job `test`: `make test`.
- Job `integration`: service containers for PostgreSQL 17 and Valkey 8 at the same pinned tags as `compose.yaml`, with health checks; the two `YUMDRAKE_TEST_*` variables set; `go test -race -tags integration ./...`.
- Job `smoke`: `make smoke`.
- Jobs cancel superseded runs on the same ref.

README sections: what Yumdrake is (two sentences and a link to the spec), prerequisites (Go 1.26, Docker, golangci-lint), `make dev-up`, running each service locally with the environment variables from Task 2, `make test`, `make test-integration`, `make smoke`, and the table of environment variables.

- [ ] **Step 1: Write** `.github/workflows/ci.yml`.
- [ ] **Step 2: Validate** it locally: if `actionlint` is installed run `actionlint`; otherwise parse it as YAML and check every `uses:` line ends in a 40-character SHA.
- [ ] **Step 3: Write** `README.md`.
- [ ] **Step 4: Run** `make lint test test-integration smoke` one last time. Expected: all pass.
- [ ] **Step 5: Commit** `chore: add CI workflow and README`.
- [ ] **Step 6: Push the branch and open a pull request** (the owner has approved pushing). Confirm all four CI jobs pass. If one fails, fix the cause and push again; do not weaken the check.

**Acceptance:** the pull request's four jobs are green. The CI run is the validation of the workflow file.

---

## Definition of done for this plan

- `make lint test test-integration smoke` passes locally and the same four jobs pass in CI.
- Every test named above exists, was seen failing first for the stated reason, and passes.
- No dependency beyond the three named.
- A reviewer confirms the five Review Focus conditions by reading the named tests, not only the diff.
