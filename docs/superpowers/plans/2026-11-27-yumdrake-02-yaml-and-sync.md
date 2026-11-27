# Yumdrake Plan 2: YAML and Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Yumdrake can read, validate and keep in sync its three kinds of YAML (environments, services, jobs) from GitHub, serve them over an API, and check them offline with `yumdrake validate`.

**Architecture:** A pure package `internal/spec` parses and validates YAML with line-numbered problems and is shared by the orchestrator and the CLI. The orchestrator polls GitHub, activates the inventory one commit at a time as a whole, keeps each job's last good version, and stores everything in PostgreSQL. GitHub credentials are encrypted before they are stored.

**Tech Stack:** Go 1.26; one new module for YAML (see Global Constraints); GitHub REST API through `net/http`; PostgreSQL through `pgx/v5`; standard `flag` package for the CLI.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 6, 11, 13, 14 and 17 item 2)

**Builds on:** Plan 1 (`internal/config`, `internal/db`, `internal/health`, `internal/app`, `cmd/orchestrator`). Where this plan names something Plan 1 produced, use the actual name in the code if it differs.

No full code, by the owner's rule: tasks give files, signatures, behaviour, test names with cases, and acceptance criteria.

## Global Constraints

- All of Plan 1's Global Constraints still apply (test first, standard `testing` only, redaction, JSON logs, `YUMDRAKE_` variables, conventional commits, `make lint test test-integration` before every commit).
- One new third-party module is allowed: a YAML v3 library. Use `go.yaml.in/yaml/v3` (the maintained continuation of `gopkg.in/yaml.v3`). Before adding it, confirm on pkg.go.dev that the module exists, is the YAML organisation's fork, and has a release in the last 12 months; if not, stop and escalate instead of picking another library.
- No GitHub SDK, no JWT library, no JSON Schema library, no CLI framework. GitHub App tokens are signed with `crypto/rsa` from the standard library.
- Every YAML file must start with `apiVersion: yumdrake/v1` and a `kind` of `Environments`, `Service` or `Job`. Unknown fields are errors.
- Step types in version 1 are exactly `copy-config` and `ecs-deploy`. `inputs`, `run` and `verify` are not part of version 1 and are rejected as unknown.
- Labels are exactly `dev`, `preprod`, `prod`.
- Every problem reported to a user carries the file, the line, and a message that says what is wrong and names the offending value. Problems are sorted by file then line.
- Secrets (GitHub tokens, App private keys) are stored only as ciphertext, never returned by any API, never logged.
- Until Plan 3 adds sign-in, every `/api/v1` route requires `Authorization: Bearer <YUMDRAKE_BOOTSTRAP_TOKEN>`. If that variable is empty the routes are not registered at all. Plan 3 removes this mechanism.
- Sync is by polling (default every 60s). No webhooks.
- Floci, the local AWS emulator (`docs/research/2026-11-27-floci.md`), is not needed by this plan. It joins the Compose stack in Plan 5.

## What Plan 1 actually produced

Use these, not the names Plan 1's text predicted:

- `go.mod` says `go 1.26.0`. Modules: `pgx/v5` v5.13.0, `goose/v3` v3.30.2, `valkey-go` v1.0.80.
- Fresh test databases come from `internal/testutil/testdb`: `testdb.New(t)`.
- `health.Handler(checks []health.Check, opts ...health.Option)`.
- `internal/app` exports `Run`, `Service`, `Healthcheck`, `PostgresCheck(pool, url)` and `ValkeyCheck`; both `cmd` packages are thin wrappers around `app.Run`.
- `config.ValidateHostPort` is exported. Exit codes: 0 clean, 1 start-up or serve failure, 2 config or usage.
- Images: `postgres:17.12-alpine`, `valkey/valkey:8.1.12`, `golang:1.26.11`, distroless static pinned by digest. CI uses golangci-lint v2.15.0.

### Task 0: Carry-over fixes from Plan 1's review

Small defects parked during Plan 1. Fix them first, each with a test written and seen failing before the fix.

**Files:** `internal/config/config.go`, `internal/db/db.go`, `internal/app/*.go`, `scripts/smoke.sh`, `compose.yaml`, and their tests.

- [ ] **One place scrubs credentials.** Today the logic exists in both `internal/app` and `internal/db`, and `config.RedactURL` ignores a password given as a `?password=` query parameter. Make `config.RedactURL` handle the user-info password, a `password` query parameter, and percent-encoded forms, add `config.Scrub(text, url string) string` that removes every form of that URL's password from arbitrary text, and have `app` and `db` call it. Tests: `TestRedactURLQueryPassword`, `TestScrubRemovesEncodedAndPlainPassword`.
- [ ] **Smoke must not wipe the development stack.** `scripts/smoke.sh` runs under its own Compose project name (`-p yumdrake-smoke`) on ports that do not clash with `make dev-up`, so its `down -v` removes only its own volumes. Check: start `make dev-up`, create a table by hand, run `make smoke`, the table is still there.
- [ ] **Images built by Compose carry the real version.** Pass `VERSION` and `COMMIT` build arguments from the Makefile to Compose builds. Check: `docker run --rm yumdrake-orchestrator:dev version` does not print `dev (unknown)` when built with `make images`.
- [ ] **Run** `make lint test test-integration smoke`, then commit `fix: scrub credentials in one place and isolate the smoke stack`.

## Review Focus

1. A YAML file with a tab, a duplicate key, or a second document: reported as a problem with a line number, not a panic and not silently accepted. (Task 1)
2. One invalid service file in a commit of otherwise good files: the whole previous inventory stays active, nothing is half-applied, and the error names the file and commit. (Task 7)
3. GitHub is down, rate-limited or returns 5xx during a poll: the active inventory and jobs are untouched, the status shows the failure and the age of the last good sync, and the next poll recovers. (Tasks 6 and 7)
4. Two orchestrator tasks polling at once: each change is applied once and there are no duplicate snapshots. (Task 7)
5. A job that was valid becomes invalid because the inventory changed under it (a service it names was removed): the job is marked not runnable with the reason, while its last good definition stays visible. (Task 7)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/spec/types.go` | The parsed model: environments, services, jobs, problems |
| `internal/spec/parse.go` | Strict YAML decoding of the three kinds, with line numbers |
| `internal/spec/inventory.go` | Cross-file inventory validation |
| `internal/spec/job.go` | Job validation against an inventory, including the step graph |
| `internal/spec/testdata/` | Valid and invalid fixtures |
| `schemas/environments.schema.json`, `schemas/service.schema.json`, `schemas/job.schema.json` | Published JSON Schemas |
| `internal/spec/schema_test.go` | Keeps the schemas in step with the Go types |
| `cmd/yumdrake/main.go` | The CLI: `validate`, `version` |
| `internal/secrets/secrets.go` | Encrypt and decrypt stored secrets |
| `internal/db/migrations/00002_definitions.sql` | Tables for connections, folders, jobs, inventory |
| `internal/store/*.go` | SQL for those tables, one file per aggregate |
| `internal/github/client.go`, `internal/github/auth.go` | Read-only GitHub access by token or App |
| `internal/sync/inventory.go`, `internal/sync/jobs.go`, `internal/sync/loop.go` | Polling and activation |
| `internal/api/*.go` | `/api/v1` handlers and the bootstrap-token guard |
| `test/e2e/sync_test.go` | Orchestrator against a fake GitHub server, end to end |

---

### Task 1: Model and strict parsing

**Files:**
- Create: `internal/spec/types.go`, `internal/spec/parse.go`, `internal/spec/testdata/valid/**`, `internal/spec/testdata/invalid/**`
- Modify: `go.mod`
- Test: `internal/spec/parse_test.go`

**Interfaces:**
- Produces:

```go
type Problem struct {
    File    string
    Line    int // 1-based; 0 when the problem is about the whole file
    Message string
}
type Problems []Problem
func (p Problems) Error() string      // one "file:line: message" per line
func (p Problems) Sorted() Problems   // by File, then Line, then Message

type Environment struct{ Name, Label, AWSAccount string; Line int }
type EnvFile struct{ Name, Source, Destination string; Line int }
type Toml struct{ Name, Source, Destination string; Line int }
type VersionRule struct{ Pattern, Sort string; Limit int }
type ServiceEnv struct {
    ECSService, ECRRepository string
    EnvFiles []EnvFile
    Tomls    []Toml
    Line     int
}
type Service struct {
    File         string
    Name         string
    Versions     VersionRule
    Environments map[string]ServiceEnv
}
type Need struct{ Step, Scope string } // Scope is "step" or "service"
type Step struct {
    Name, Use, Group string
    MaxParallel      int           // 0 means not set
    Timeout          time.Duration // 0 means not set
    Needs            []Need
    Line             int
}
type Group struct{ ID, Label string; Services []string; Line int } // in file order
type Approval struct{ Labels []string; Expires time.Duration }
type Job struct {
    File         string
    ID, Title    string
    Labels       []string
    Groups       []Group
    Approval     *Approval
    SlackChannel string
    Steps        []Step
}

func ParseEnvironments(file string, data []byte) ([]Environment, Problems)
func ParseService(file string, data []byte) (Service, Problems)
func ParseJob(file string, data []byte) (Job, Problems)
```

Behaviour:
- Parsing checks shape only: `apiVersion` and `kind`, known fields, required fields present, scalar types, and the value formats in the table below. Checks that need another file belong to Tasks 2 and 3.
- `needs` accepts a list whose items are either a string (`a`, meaning scope `step`) or a mapping with `step` and optional `scope`.
- Defaults applied by the parser: `VersionRule` `{Pattern: ^v?\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$, Sort: "semver", Limit: 100}`; `Approval.Expires` 24h; `Need.Scope` `"step"`.
- A parse never panics on any input. Empty input, a tab used for indentation, duplicate keys, more than one YAML document, and a non-mapping root are each one problem with a line number.
- Durations use Go syntax (`24h`, `15m`).

Formats checked while parsing:

| Field | Rule |
|---|---|
| Names: environment `name`, service `name`, job `job`, group id, step `name` | `^[a-z0-9][a-z0-9-]{0,62}$` |
| `label` and every label in `environment.labels`, `approval.when.labels` | `dev`, `preprod` or `prod` |
| `aws_account` | exactly 12 digits, written as a quoted string |
| `ecs_service` | `^arn:aws:ecs:[a-z0-9-]+:\d{12}:service/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$` |
| `ecr_repository` | `^\d{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/[a-z0-9._/-]+$` |
| `source_env_path` | at least three `/`-separated non-empty parts: owner, repo, path |
| `destination_env_path`, `source_toml_path`, `destination_toml_path` | `s3://<bucket>/<key>` with non-empty bucket and key |
| `versions.pattern` | compiles as a Go regular expression |
| `versions.sort` | `semver` or `pushed` |
| `versions.limit` | 1 to 1000 |
| step `use` | `copy-config` or `ecs-deploy` |
| step `max_parallel` | at least 1 |
| step `timeout` | greater than zero, allowed only on `ecs-deploy` |
| `needs[].scope` | `step` or `service` |
| `notify.slack` | starts with `#` |

- [ ] **Step 1: Create fixtures.** Under `testdata/valid/`: `environments.yaml` (the five environments of the spec's example: three `dev`, one `preprod`, one `prod`, across three account ids), `services/backend-api.yaml` (two environments, one with an env file and a TOML, one with neither), `services/orders.yaml`, `jobs/deploy-backend.yaml` (two groups, four steps, one `needs` in each of the three accepted forms). Under `testdata/invalid/`, one small file per case in Step 2.
- [ ] **Step 2: Write the failing tests** in `parse_test.go`:
  - `TestParseValidFixtures`: each valid fixture parses with no problems; spot-check that `backend-api` has two environments, that the env file's `Source` and `Destination` equal the fixture's values, and that the job's `Needs` have the expected `Step` and `Scope`.
  - `TestParseAppliesDefaults`: a service with no `versions` gets the default rule; a job with `approval` but no `expires` gets 24h; a string `needs` item gets scope `step`.
  - `TestParseReportsLineNumbers`: a job whose step on line 14 has `use: build`; the single problem has `Line` 14 and a message containing `build`.
  - `TestParseRejections`, table-driven, one row per case, each asserting at least one problem whose message contains the given fragment: missing `apiVersion`; `apiVersion: yumdrake/v2`; wrong `kind` for the function called; unknown top-level field `inputs` in a job; unknown field inside a step; step `use: run`; step `use: verify`; label `staging`; `aws_account` written as a bare number; `aws_account` of 11 digits; `ecs_service` that is a bare name; `ecr_repository` without a registry host; `source_env_path` with two parts; destination not starting `s3://`; `versions.pattern` `(`; `versions.limit` 0; `max_parallel` 0; `timeout` on a `copy-config` step; `needs` scope `leg`; `notify.slack` without `#`; upper-case job id.
  - `TestParseNeverPanics`, table-driven over hostile inputs: empty; only a comment; a tab-indented mapping; a duplicate key; two documents separated by `---`; a sequence at the root; 1 MB of `a: ` nesting. Each returns at least one problem and does not panic. (Review Focus 1)
  - `TestProblemsSortedAndFormatted`: three problems out of order format as three `file:line: message` lines in order.
- [ ] **Step 3: Run** `go test ./internal/spec/` and confirm it fails because the package does not exist.
- [ ] **Step 4: Add** the YAML module after the check in Global Constraints. Implement `types.go` and `parse.go`. Decode through the YAML node tree so every value's line is known and unknown or duplicate keys are caught.
- [ ] **Step 5: Run** `make lint test`. Expected: pass.
- [ ] **Step 6: Commit** `feat: add strict parsing of environment, service and job YAML`.

**Acceptance:** all six tests pass; no input makes a parse function panic.

---

### Task 2: Inventory validation

**Files:**
- Create: `internal/spec/inventory.go`
- Test: `internal/spec/inventory_test.go`

**Interfaces:**
- Consumes: Task 1 types.
- Produces:

```go
type Inventory struct {
    Environments []Environment        // in file order
    Services     map[string]Service
}
func (inv Inventory) Environment(name string) (Environment, bool)
func (inv Inventory) ServiceNames() []string // sorted
func BuildInventory(envFile string, envs []Environment, services []Service) (Inventory, Problems)
```

Rules, each a problem naming the file, line and value:
- Environment names are unique.
- At least one environment exists.
- Service names are unique across files, and a service's `name` equals its file's base name without `.yaml`.
- Every key under a service's `environments` is a known environment.
- The account id inside `ecs_service` equals that environment's `aws_account`. (`ecr_repository` may be in another account.)
- Within one service environment, env file names are unique and TOML names are unique.
- Within one environment, no two destinations (env or TOML, from any service) are the same S3 URI.
- A service with no environments at all is a problem.

- [ ] **Step 1: Write the failing tests**:
  - `TestBuildInventoryAcceptsValidFixtures`: the Task 1 valid fixtures build with no problems; `ServiceNames()` is sorted; `Environment("hetzner-01")` is found.
  - `TestBuildInventoryRejections`, table-driven, one row per rule above, each built by mutating the valid model in the test, asserting the fragment and that `File` is the offending file.
  - `TestBuildInventoryReportsAllProblemsAtOnce`: two independent mistakes in two files yield two problems.
- [ ] **Step 2: Run** `go test ./internal/spec/` and confirm the new tests fail because `BuildInventory` is undefined.
- [ ] **Step 3: Implement** `inventory.go`.
- [ ] **Step 4: Run** `make lint test`. Expected: pass.
- [ ] **Step 5: Commit** `feat: validate the inventory across files`.

**Acceptance:** the three tests pass.

---

### Task 3: Job validation and the step graph

**Files:**
- Create: `internal/spec/job.go`
- Test: `internal/spec/job_test.go`

**Interfaces:**
- Consumes: Task 1 types, Task 2 `Inventory`.
- Produces:

```go
func ValidateJob(job Job, inv Inventory) Problems
func (j Job) Group(id string) (Group, bool)
func (j Job) Step(name string) (Step, bool)
func (j Job) ServicesIn(envName string, inv Inventory) map[string][]string // group id -> services that exist in that environment, in group order
func (j Job) StepOrder() [][]string // step names in dependency layers; only valid when ValidateJob returned none
```

Rules:
- `environment.labels` is non-empty and has no duplicates.
- At least one group; each group has at least one service; group ids unique.
- Every service named exists in the inventory. A service appears at most once in the whole job.
- At least one step; step names unique; every step has a `group` that exists.
- Every `needs` names an existing step other than itself. The graph has no cycle; a cycle is reported once, listing the steps in it.
- A `needs` with scope `service` requires both steps to use the same group.
- `approval.when.labels` is a subset of `environment.labels`.
- A job with a group whose services exist in none of the job's permitted environments is a problem (it could never deploy anything).

- [ ] **Step 1: Write the failing tests**:
  - `TestValidateJobAcceptsValidFixture`.
  - `TestValidateJobRejections`, table-driven, one row per rule, each asserting fragment and line.
  - `TestValidateJobReportsCycleOnce`: steps a needs b, b needs c, c needs a; exactly one problem, mentioning a, b and c.
  - `TestStepOrderLayers`: for the fixture (`config-backends`, `roll-backends` needing it, `config-microservices`, `roll-microservices` needing it), `StepOrder()` is `[[config-backends config-microservices] [roll-backends roll-microservices]]`, each layer sorted by name.
  - `TestServicesInOmitsServicesMissingFromEnvironment`: a group of three services where one has no block for `prod-01`; `ServicesIn("prod-01", inv)` lists the other two in group order.
- [ ] **Step 2: Run** and confirm the new tests fail because `ValidateJob` is undefined.
- [ ] **Step 3: Implement** `job.go`.
- [ ] **Step 4: Run** `make lint test`. Expected: pass.
- [ ] **Step 5: Commit** `feat: validate jobs and their step graph against the inventory`.

**Acceptance:** the five tests pass.

---

### Task 4: JSON Schemas and the CLI

**Files:**
- Create: `schemas/environments.schema.json`, `schemas/service.schema.json`, `schemas/job.schema.json`, `internal/spec/schema_test.go`, `cmd/yumdrake/main.go`, `cmd/yumdrake/main_test.go`
- Modify: `Makefile` (`build` also builds `bin/yumdrake`), `README.md` (a "Validating YAML" section)

**Interfaces:**
- Consumes: Tasks 1 to 3.
- Produces: `func run(args []string, stdout, stderr io.Writer) int` in `cmd/yumdrake`, and:

```go
// internal/spec
func LoadDir(inventoryDir string, jobFiles []string) (Inventory, []Job, Problems)
```

`LoadDir` reads `<inventoryDir>/environments.yaml` and every `<inventoryDir>/services/*.yaml`, builds the inventory, then parses and validates each job file against it. A missing `environments.yaml` is a problem, not an error return.

Schemas: JSON Schema draft 2020-12, `additionalProperties: false` everywhere, the patterns and enums of Task 1's table, `$id` `https://yumdrake.dev/schemas/<kind>.schema.json`. They exist for editor completion; the Go validator is the authority.

CLI:
- `yumdrake validate <inventory-dir> [--job <file>]...` prints each problem as `file:line: message` to stderr, then a one-line summary to stdout: `OK: 5 environments, 2 services, 1 jobs` or `FAILED: 3 problems`. Exit 0 valid, 1 problems found, 2 usage error.
- `yumdrake version` prints `version.String()`.
- `yumdrake -h` and an unknown command print usage; exit 0 and 2.

- [ ] **Step 1: Write the failing tests**:
  - `schema_test.go` `TestSchemasListEveryYAMLField`: for each kind, walk the YAML keys the parser accepts (export a small `spec.KnownFields(kind string) []string` for this, returning dotted paths) and assert each appears as a property at the matching path in the schema file, and that the schema has no property the parser does not accept.
  - `TestSchemasAreValidJSONWithStrictObjects`: each file parses as JSON and every object schema has `additionalProperties` false.
  - `main_test.go` `TestValidateValidTreeExits0` using `internal/spec/testdata/valid`; stdout is `OK: 5 environments, 2 services, 1 jobs`.
  - `TestValidateInvalidTreeExits1AndPrintsFileLine`: a temp copy with one broken service; stderr has a line matching `services/.*\.yaml:\d+: `; stdout starts `FAILED: `.
  - `TestValidateMissingDirectoryExits1`, `TestUsageErrorsExit2` (no arguments; unknown command; `validate` with no directory), `TestVersion`.
- [ ] **Step 2: Run** and confirm they fail because the files do not exist.
- [ ] **Step 3: Implement** `spec.KnownFields`, `spec.LoadDir`, the three schema files and the CLI.
- [ ] **Step 4: Run** `make lint build test`, then `bin/yumdrake validate internal/spec/testdata/valid --job internal/spec/testdata/valid/jobs/deploy-backend.yaml`. Expected: `OK: 5 environments, 2 services, 1 jobs`.
- [ ] **Step 5: Commit** `feat: add yumdrake validate and published JSON Schemas`.

**Acceptance:** the seven tests pass and the command in Step 4 prints the expected line.

---

### Task 5: Secret encryption

**Files:**
- Create: `internal/secrets/secrets.go`
- Modify: `internal/config/config.go` (new variables), `README.md` (variable table)
- Test: `internal/secrets/secrets_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:

```go
type Cipher interface {
    Encrypt(ctx context.Context, purpose string, plaintext []byte) ([]byte, error)
    Decrypt(ctx context.Context, purpose string, ciphertext []byte) ([]byte, error)
}
func NewLocal(key []byte) (Cipher, error) // AES-256-GCM, key must be 32 bytes
```

Behaviour:
- `purpose` (for example `github-connection`) is bound into the ciphertext as additional authenticated data: decrypting with a different purpose fails.
- Each ciphertext starts with a one-byte format version, then a random nonce. Encrypting the same plaintext twice gives different bytes.
- A tampered or truncated ciphertext returns an error and never a partial plaintext.
- The local cipher is for development and tests. The KMS-backed one arrives with Plan 5 and implements the same interface.

New configuration:

| Variable | Used by | Default | Rule |
|---|---|---|---|
| `YUMDRAKE_SECRETS_MODE` | both | `local` | `local` only in this plan; any other value is a config error |
| `YUMDRAKE_LOCAL_SECRET_KEY` | both | none, required when mode is `local` | base64 of exactly 32 bytes; never printed |
| `YUMDRAKE_BOOTSTRAP_TOKEN` | orchestrator | empty | if set, at least 32 characters; never printed |
| `YUMDRAKE_SYNC_INTERVAL` | orchestrator | `60s` | Go duration of at least `10s` |
| `YUMDRAKE_GITHUB_API_URL` | orchestrator | `https://api.github.com` | an `http` or `https` URL; overridden in tests |

- [ ] **Step 1: Write the failing tests** in `secrets_test.go`: `TestRoundTrip`; `TestSamePlaintextEncryptsDifferently`; `TestWrongPurposeFails`; `TestTamperedCiphertextFails` (flip one byte at three positions); `TestTruncatedCiphertextFails`; `TestNewLocalRejectsWrongKeyLength` (16 and 33 bytes).
- [ ] **Step 2: Write the failing tests** in `config_test.go`: the new defaults; a key that is not base64 or not 32 bytes is reported; a 10-character bootstrap token is reported; neither the key nor the token appears in any error text.
- [ ] **Step 3: Run** and confirm failure for the missing package and fields.
- [ ] **Step 4: Implement** both.
- [ ] **Step 5: Run** `make lint test`. Expected: pass.
- [ ] **Step 6: Commit** `feat: encrypt stored secrets and add sync configuration`.

**Acceptance:** the tests pass; no secret value appears in any error or log.

---

### Task 6: Storage and the GitHub client

Two independent halves; they may be given to two implementers.

**Files:**
- Create: `internal/db/migrations/00002_definitions.sql`, `internal/store/connections.go`, `internal/store/folders.go`, `internal/store/jobs.go`, `internal/store/inventory.go`, `internal/github/client.go`, `internal/github/auth.go`
- Test: `internal/store/store_integration_test.go`, `internal/github/client_test.go`

**Interfaces:**
- Consumes: Task 5 `secrets.Cipher`, Plan 1 `*pgxpool.Pool`.
- Produces, storage (`package store`; every function takes `ctx` and a `*pgxpool.Pool` or `pgx.Tx` through one small `DBTX` interface):

```go
type Connection struct{ ID int64; Owner, Method string; AppID, InstallationID int64; CreatedAt time.Time } // Method: "app" | "token"
func CreateConnection(ctx, db, c Connection, secretCiphertext []byte) (Connection, error) // ErrDuplicate on owner
func ListConnections(ctx, db) ([]Connection, error)
func ConnectionSecret(ctx, db, owner string) (Connection, []byte, error)                  // ErrNotFound

type Folder struct{ ID int64; Name string }
func CreateFolder(ctx, db, name string) (Folder, error) // ErrDuplicate, case-insensitive
func ListFolders(ctx, db) ([]Folder, error)

type JobSource struct {
    ID                       int64
    FolderID                 int64
    Repo, Branch, Path       string // Repo is "owner/name"
    JobID, Title             string // empty until first good read
    LastGood                 []byte // JSON of spec.Job, nil until first good read
    LastGoodCommit           string
    Runnable                 bool
    Problems                 []byte // JSON of spec.Problems for the newest commit, nil when clean
    ProblemsCommit           string
    CheckedAt, SyncedAt      *time.Time
}
func CreateJobSource(ctx, db, s JobSource) (JobSource, error) // ErrDuplicate on (repo, branch, path)
func ListJobSources(ctx, db) ([]JobSource, error)
func GetJobSource(ctx, db, id int64) (JobSource, error)
func MoveJobSource(ctx, db, id, folderID int64) error
func SaveJobGood(ctx, db, id int64, job []byte, jobID, title, commit string, at time.Time) error // ErrDuplicate if jobID is used by another source
func SaveJobProblems(ctx, db, id int64, problems []byte, commit string, runnable bool, at time.Time) error
func TouchJobChecked(ctx, db, id int64, at time.Time) error

type InventorySource struct{ Repo, Branch, Path string }
func SetInventorySource(ctx, db, s InventorySource) error
func GetInventorySource(ctx, db) (InventorySource, bool, error)
type Snapshot struct{ ID int64; Commit string; Inventory []byte; ActivatedAt time.Time } // Inventory is JSON of spec.Inventory
func ActivateSnapshot(ctx, db, commit string, inventory []byte, at time.Time) (Snapshot, error) // no-op returning the existing row if commit is already active
func ActiveSnapshot(ctx, db) (Snapshot, bool, error)
type InventoryStatus struct{ CheckedAt *time.Time; Problems []byte; ProblemsCommit string }
func SaveInventoryStatus(ctx, db, st InventoryStatus) error
func GetInventoryStatus(ctx, db) (InventoryStatus, error)

var ErrNotFound, ErrDuplicate error
```

- Produces, GitHub (`package github`):

```go
type Credentials struct {
    Method         string // "token" | "app"
    Token          string
    AppID          int64
    InstallationID int64
    PrivateKeyPEM  []byte
}
type Client struct{ /* unexported */ }
func New(baseURL string, httpClient *http.Client, creds Credentials) (*Client, error)
func (c *Client) HeadCommit(ctx, repo, branch string) (sha string, err error)
func (c *Client) ListFiles(ctx, repo, commit, dir string) ([]string, error) // paths under dir, recursive, files only
func (c *Client) ReadFile(ctx, repo, commit, path string) ([]byte, error)
func (c *Client) BranchExists(ctx, repo, branch string) (bool, error)
var ErrNotFound error
type RateLimitError struct{ RetryAfter time.Duration }
```

Storage behaviour:
- Migration `00002` creates `github_connections`, `folders`, `job_sources`, `inventory_snapshots` and a single-row `inventory_status`, with the uniqueness rules the functions above rely on, and a partial unique index so at most one snapshot is active. The inventory source lives in `instance_settings` under key `inventory_source`.
- `ActivateSnapshot` deactivates the previous snapshot and activates the new one in one transaction. Old snapshots are kept.

GitHub behaviour:
- Read-only. Sends `Accept: application/vnd.github+json` and a `User-Agent` of `yumdrake`.
- Token method sends the token as a bearer. App method signs an RS256 JWT (issuer the App id, lifetime 9 minutes), exchanges it for an installation token, caches that until one minute before it expires, and renews it on demand.
- 404 maps to `ErrNotFound`. 403 or 429 with rate-limit headers maps to `*RateLimitError`. 5xx and network errors are returned as errors that do not contain the token.
- `HeadCommit` and `ListFiles` send `If-None-Match` with the last ETag seen for that URL and reuse the cached result on 304.

- [ ] **Step 1: Write the failing storage tests** (integration, each on a fresh database as in Plan 1):
  - `TestConnectionsRoundTripAndSecretStaysCiphertext`: the stored column equals the ciphertext passed in, and `ListConnections` has no secret field.
  - `TestDuplicateOwnerIsErrDuplicate`, `TestFolderNamesAreCaseInsensitivelyUnique`.
  - `TestJobSourceLifecycle`: create, save good, save problems with `runnable` false, read back every field; `TestJobIDIsUniqueAcrossSources`.
  - `TestActivateSnapshotKeepsOneActive`: activate A then B; `ActiveSnapshot` is B and exactly one row is active; `TestActivateSameCommitTwiceIsNoOp`.
  - `TestActivateSnapshotConcurrentlyKeepsOneActive`: eight goroutines activate eight commits; exactly one row is active afterwards. (Review Focus 4)
- [ ] **Step 2: Write the failing GitHub tests** against an `httptest.Server` that mimics the REST endpoints used:
  - `TestTokenAuthSendsBearer`; `TestHeadCommit`; `TestListFilesIsRecursiveAndScopedToDir`; `TestReadFileAtCommit` (content is base64 in the API response); `TestBranchExists` true and false.
  - `TestNotFoundMapsToErrNotFound`; `TestRateLimitMapsToRateLimitErrorWithRetryAfter`; `TestServerErrorDoesNotLeakToken`.
  - `TestConditionalRequestReusesCachedResultOn304`: the server counts requests and returns 304 the second time; the second call returns the same value.
  - `TestAppAuthSignsJWTAndCachesInstallationToken`: generate an RSA key in the test; the server verifies the JWT signature and issuer with the public key and hands out a token valid for 10 minutes; two API calls cause exactly one token exchange; with a token expiring in 30 seconds the next call exchanges again.
- [ ] **Step 3: Run** `make test test-integration` and confirm failure for the missing packages.
- [ ] **Step 4: Implement** the migration, `internal/store` and `internal/github`.
- [ ] **Step 5: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 6: Commit** twice: `feat: store connections, folders, job sources and inventory snapshots` and `feat: add read-only GitHub client with token and App auth`.

**Acceptance:** all tests above pass; `db.LatestVersion()` is 2.

---

### Task 7: Sync engine

**Files:**
- Create: `internal/sync/inventory.go`, `internal/sync/jobs.go`, `internal/sync/loop.go`
- Test: `internal/sync/sync_integration_test.go`

**Interfaces:**
- Consumes: `spec`, `store`, `github`, `secrets`.
- Produces:

```go
type Source interface { // satisfied by *github.Client; faked in tests
    HeadCommit(ctx context.Context, repo, branch string) (string, error)
    ListFiles(ctx context.Context, repo, commit, dir string) ([]string, error)
    ReadFile(ctx context.Context, repo, commit, path string) ([]byte, error)
}
type SourceFor func(ctx context.Context, owner string) (Source, error) // looks up and decrypts the connection for a repo owner

type Engine struct{ /* unexported */ }
func NewEngine(pool *pgxpool.Pool, sources SourceFor, log *slog.Logger, now func() time.Time) *Engine
func (e *Engine) SyncInventory(ctx context.Context) error
func (e *Engine) SyncJob(ctx context.Context, jobSourceID int64) error
func (e *Engine) SyncAll(ctx context.Context) error            // inventory, then every job
func (e *Engine) Run(ctx context.Context, interval time.Duration) // loops SyncAll until ctx ends
```

Behaviour:
- `SyncInventory`: read the head commit of the inventory source. If it is the active snapshot's commit, only record the check time. Otherwise read `environments.yaml` and every `services/*.yaml` under the path at that commit, parse and `BuildInventory`. No problems: `ActivateSnapshot` and clear the status problems. Any problem: leave the active snapshot alone and save the problems with the commit. (Review Focus 2)
- After a new snapshot is activated, every job's last good definition is validated again against it. A job that no longer validates gets `Runnable` false and the reasons; its `LastGood` is kept. (Review Focus 5)
- `SyncJob`: read the file at the branch head. Unchanged commit and unchanged inventory: record the check time. Otherwise parse and `ValidateJob` against the active inventory. Valid: `SaveJobGood`, runnable true. Invalid: keep the previous `LastGood`, save the problems. With no active inventory, the problem is `no active inventory yet`.
- A failure to reach GitHub (network, 5xx, rate limit, missing connection for the owner) never changes stored definitions. It is logged at warn level and returned; `SyncAll` continues with the remaining jobs and returns the joined errors. A `RateLimitError` makes `Run` wait for its `RetryAfter` before the next round. (Review Focus 3)
- `Run` takes a PostgreSQL advisory lock with `pg_try_advisory_lock` on a dedicated connection for each round and skips the round if another process holds it, so only one orchestrator polls at a time. (Review Focus 4)
- No inventory source configured: `SyncInventory` does nothing and returns nil.

- [ ] **Step 1: Write the failing tests** (integration; a fake `Source` backed by an in-memory map of `repo@commit/path` to bytes, with a settable head and a settable error):
  - `TestFirstSyncActivatesInventoryAndJob`.
  - `TestUnchangedCommitOnlyRecordsCheckTime`: the snapshot id is unchanged and `CheckedAt` moved.
  - `TestInvalidFileKeepsPreviousInventoryWhole`: a new commit where one of three service files is broken; the active snapshot is still the old commit, with all its services; the status problems name the broken file and the new commit. (Review Focus 2)
  - `TestFixedCommitActivatesAndClearsProblems`.
  - `TestJobInvalidAgainstInventoryKeepsLastGood`: a new job commit naming an unknown service; `LastGood` and `LastGoodCommit` are unchanged, problems are saved.
  - `TestInventoryChangeMakesJobNotRunnable`: a new, valid inventory commit without a service the job uses; the job has `Runnable` false, a reason naming the service, and its `LastGood` intact. (Review Focus 5)
  - `TestSourceErrorChangesNothingAndIsReturned`: the fake returns an error; every stored row is byte-for-byte as before; `SyncAll` returns an error and still attempted the other jobs. (Review Focus 3)
  - `TestRunSkipsRoundWhenAnotherHolderHasTheLock`: hold the advisory lock on another connection; one `Run` round performs no `HeadCommit` calls; release it and the next round does. (Review Focus 4)
  - `TestNoInventorySourceIsNotAnError`.
- [ ] **Step 2: Run** `make test-integration` and confirm failure for the missing package.
- [ ] **Step 3: Implement** the three files.
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: sync inventory and jobs from GitHub with whole-commit activation`.

**Acceptance:** the nine tests pass.

---

### Task 8: API and orchestrator wiring

**Files:**
- Create: `internal/api/api.go`, `internal/api/guard.go`, `internal/api/definitions.go`
- Modify: `cmd/orchestrator/main.go` (build the cipher, engine and API; start `Engine.Run`), `README.md`
- Test: `internal/api/api_integration_test.go`

**Interfaces:**
- Consumes: `store`, `sync.Engine`, `secrets.Cipher`, `config`.
- Produces: `func api.New(deps api.Deps) http.Handler`, where `Deps` holds the pool, the engine, the cipher, the bootstrap token and a logger. The orchestrator mounts it at `/api/v1/` next to the health handler.

Routes (JSON in and out; errors are `{"error":{"code":"...","message":"..."}}` with codes `unauthorized`, `not_found`, `invalid`, `conflict`, `internal`):

| Method and path | Does |
|---|---|
| `GET /api/v1/inventory` | Active commit, activation time, last check time, problems of a newer rejected commit if any, environments, and services (name, environments it exists in) |
| `GET /api/v1/inventory/services/{name}` | The full parsed service |
| `PUT /api/v1/admin/inventory-source` | Set repo, branch, path; then sync once |
| `POST /api/v1/inventory/sync` | Sync the inventory now |
| `GET /api/v1/folders`, `POST /api/v1/folders` | List; create by name |
| `GET /api/v1/jobs` | Every job source: id, job id, title, folder, repo, branch, path, runnable, commit, sync and check times, problem count |
| `POST /api/v1/jobs` | Register a job source (folder id, repo, branch, path), then sync it once and return it |
| `GET /api/v1/jobs/{id}` | The source plus its last good definition and current problems |
| `PATCH /api/v1/jobs/{id}` | Move to another folder |
| `POST /api/v1/jobs/{id}/sync` | Sync that job now |
| `GET /api/v1/admin/github-connections`, `POST /api/v1/admin/github-connections` | List without secrets; create (owner, method, and token or App id, installation id and private key), encrypting the secret with purpose `github-connection` |

Behaviour:
- The guard compares the bearer token in constant time. Missing or wrong: 401 with code `unauthorized`. With an empty configured token, `api.New` returns a handler that answers 404 for everything.
- Request bodies are limited to 1 MB and reject unknown JSON fields with `invalid`.
- No response ever contains a token, a private key or ciphertext.
- An internal error is logged with its detail and answered with a generic `internal` message.

- [ ] **Step 1: Write the failing tests** (integration, with a fake `Source`):
  - `TestRoutesRequireBootstrapToken` (no header, wrong token, right token); `TestEmptyBootstrapTokenDisablesAPI`.
  - `TestCreateConnectionEncryptsAndNeverReturnsSecret`: the stored bytes are not the plaintext, decrypt to it with the right purpose, and the token string appears in no response body.
  - `TestSetInventorySourceSyncsAndInventoryIsServed`.
  - `TestRegisterJobSyncsAndReturnsDefinition`; `TestRegisterSameFileTwiceIsConflict`; `TestMoveJobBetweenFolders`; `TestUnknownJobIs404`.
  - `TestInventoryShowsProblemsOfRejectedCommitAlongsideActiveOne`.
  - `TestUnknownJSONFieldIsInvalid`; `TestOversizedBodyIsRejected`.
- [ ] **Step 2: Run** `make test-integration` and confirm failure for the missing package.
- [ ] **Step 3: Implement** the API and wire the orchestrator. `SourceFor` in the orchestrator loads the connection for the owner, decrypts it, and returns a cached `*github.Client` built with `YUMDRAKE_GITHUB_API_URL`.
- [ ] **Step 4: Run** `make lint build test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: serve inventory and jobs over the API and start the sync loop`.

**Acceptance:** the eleven tests pass; the orchestrator still starts with no bootstrap token and no inventory source configured.

---

### Task 9: End-to-end check

**Files:**
- Create: `test/e2e/sync_test.go`, `test/e2e/fakegithub/server.go`
- Modify: `Makefile` (`test-e2e`), `.github/workflows/ci.yml` (run it in the integration job), `scripts/smoke.sh` (set the new required variables)

**Interfaces:**
- Consumes: the built orchestrator's `run` function, the API.
- Produces: `make test-e2e`, running `go test -tags e2e ./test/e2e/...` against the Compose PostgreSQL and Valkey.

`fakegithub` is a small `httptest`-based server exposing the same REST endpoints `internal/github` calls, serving files from a directory tree per commit, with a method to move a branch head and a switch that makes it answer 503.

- [ ] **Step 1: Write the failing test** `TestInventoryAndJobFlowEndToEnd`: start the orchestrator's `run` in-process with a bootstrap token, a 10s sync interval and the fake GitHub URL. Through the API: create a token connection, a folder, set the inventory source, register the fixture job. Assert the inventory and the job are served and runnable. Move the inventory head to a commit with a broken service file: within two intervals the API still serves the old inventory and shows the new commit's problems. Move the head to a fixed commit: within two intervals it is active and the problems are gone. Switch the fake to 503 for one interval: nothing changes and the check time does not advance; switch back and it advances.
- [ ] **Step 2: Run** `make test-e2e` and confirm it fails because the target and package do not exist.
- [ ] **Step 3: Implement** the fake, the test and the Make target; add the new variables to Compose and the smoke script; add the e2e step to CI.
- [ ] **Step 4: Run** `make lint test test-integration test-e2e smoke`. Expected: all pass.
- [ ] **Step 5: Commit** `test: add end-to-end sync test against a fake GitHub`.
- [ ] **Step 6: Push the branch, open a pull request against the previous plan's branch, and confirm CI passes.** Do not merge it.

**Acceptance:** `make test-e2e` passes locally and in CI.

---

## Parallel execution

Tasks 1, 5 and the GitHub half of Task 6 do not depend on each other. Then Tasks 2 and 3 (same package, different files; run 2 before 3), the storage half of Task 6, and Task 4 after 3. Then 7, 8, 9 in order.

## Definition of done

- `make lint test test-integration test-e2e smoke` passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- Exactly one new third-party module.
- A reviewer confirms the five Review Focus conditions from the named tests.
