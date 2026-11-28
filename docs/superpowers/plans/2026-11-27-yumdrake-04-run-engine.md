# Yumdrake Plan 4: Run Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A run can be created, scheduled, executed by runners, watched live, cancelled, recovered after a crash and reconciled when its outcome is unknown, with whole-deployment locks and durable logs. Proven end to end with scripted fake steps; real AWS steps arrive in Plans 5 and 6.

**Architecture:** The orchestrator owns every run's state in PostgreSQL and advances it with one transactional function driven by a pure decision function. Runners claim legs under a lease with a generation number, execute them through an `Executor` interface, record intents before external changes, and write log lines in acknowledged batches. Valkey carries wake-up signals only.

**Tech Stack:** Go 1.26, PostgreSQL through `pgx/v5`, Valkey pub/sub through `valkey-go`, Server-Sent Events with `net/http`. No new third-party module.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 4 D1 to D7, 7, 8 "recovery" paragraphs, 13, 14, 16)

**Builds on:** Plans 1 to 3. Use the actual names in the code where they differ from this plan's assumptions.

No full code, by the owner's rule.

## Global Constraints

- All earlier plans' Global Constraints still apply. No new third-party module.
- Run states: `resolving`, `planned`, `waiting_approval`, `queued`, `running`, `cancelling`, `needs_reconciliation`, and the final states `succeeded`, `partially_succeeded`, `failed`, `cancelled`, `rejected`, `expired`, `invalidated`.
- Leg states: `pending`, `queued`, `claimed`, `running`, `succeeded`, `failed`, `skipped`, `cancelled`, `unknown`. The last five are settled.
- Leg kinds: `resolve`, `revalidate`, `step`.
- A run is planned first and started second. Resolving produces the plan a person looks at; nothing proceeds past `planned` until that person starts it, unless the run was created with auto-start (the CLI's `run` and the UI's "Start" on an already shown plan). A `planned` run nobody starts expires after 1 hour. The spec's state list predates this; `planned` is this plan's addition and the spec is updated in Task 4.
- Result precedence once every leg is settled: any `unknown` gives `needs_reconciliation`; else a cancel request gives `cancelled`; else all step legs `succeeded` gives `succeeded`; else at least one step leg `succeeded` gives `partially_succeeded`; else `failed`.
- A leg that failed is never retried automatically. A leg whose lease expired is given to another runner at most 5 times, then becomes `unknown`.
- Every write a runner makes to a leg is conditional on the generation it was given when it claimed the leg.
- Locks are taken all at once or not at all, before any step leg runs, and are released only when the run reaches a final state. `needs_reconciliation` keeps them.
- A log line counts as stored once its batch is committed to PostgreSQL. A repeated batch is dropped by the primary key, never duplicated.
- Defaults, each overridable by an environment variable named in its task: lease 30s, heartbeat 10s, runner poll 2s, orchestrator sweep 2s, runner concurrency 8, at most 10 running legs per environment and 20 per AWS account.
- Fake steps are for development and tests only: the runner registers them only when `YUMDRAKE_ENV=dev` and `YUMDRAKE_FAKE_STEPS=true`, and refuses to start with `YUMDRAKE_FAKE_STEPS=true` in any other environment.
- Every state change a person causes (create, cancel, approve, reject, reconcile, release locks) checks permission on the server and writes an audit entry in the same transaction.

## What Plans 1 to 3 actually produced

Use these, not the names this plan's text predicted. The earlier plans' ledgers and manager reports under `.superpowers/sdd/` have the full list.

- Migrations 00001 to 00004 exist, and **00005 is reserved** for a small change being made in parallel on the web branch (raw YAML source columns). **This plan's migration is 00006.** `db.LatestVersion()` becomes 6 once both are merged; until the web branch is merged into this one, tests that assert the latest version must not hard-code it.
- API: middleware methods are on `*server`. Every new route must be added to `server.routes()`, to the expected map in `TestEveryRouteEnforcesItsAction`, and, for list GETs, to `TestNoRouteReturnsNullForAList`; otherwise the guard answers 404 for it. Every JSON list is `[]`, never `null`. Audited writes go through `auditedWrite`.
- Valkey: every key must use `Deps.KeyPrefix` (`YUMDRAKE_VALKEY_KEY_PREFIX`), because concurrent test runs share one Valkey. This applies to the wake-up topics in Task 8.
- `auth.Principal`, `auth.Target`, `Principal.Can` and `MayApproveRunStartedBy` exist as planned. `store.ErrInvalid`, `store.ErrNotFound`, `store.ErrDuplicate`, `store.ErrInvalidReference` exist. `AuditEntry.Detail` is `json.RawMessage`.
- Wiring lives in `internal/app` (`app.NewOrchestratorService`, `OrchestratorExtension`, `Service.Extend`); add the engine loop and the runner's worker pool there.
- Required variables now include `YUMDRAKE_PUBLIC_URL` and `YUMDRAKE_LOCAL_SECRET_KEY`. `YUMDRAKE_BOOTSTRAP_TOKEN` is gone: end-to-end tests create an admin and sign in (see `test/e2e/access_test.go`).
- Test databases: `internal/testutil/testdb`. The store truncates times to microseconds.
- Modules so far: `pgx/v5` v5.13.0, `goose/v3` v3.30.2, `valkey-go` v1.0.80, `go.yaml.in/yaml/v3` v3.0.6, `golang.org/x/crypto` v0.59.0, `crewjam/saml` v0.6.3.

## Review Focus

1. A runner is killed after its external change succeeded but before it recorded the result: the next runner sees the recorded intent, does not blindly repeat the change, and the leg ends succeeded, failed or `unknown`, never silently lost. (Tasks 5, 6, 10)
2. Run A and run B target the same service: B does nothing to it until A is final, including while A sits in `needs_reconciliation`. (Tasks 4, 10)
3. A stale runner that lost its lease keeps running and tries to report success: its write is refused and the newer attempt's result stands. (Task 5)
4. Cancel arrives while legs are in every possible state: nothing pending starts, in-flight legs settle truthfully, and the run reports what was changed. (Tasks 3, 4, 10)
5. The browser's event stream drops and reconnects mid-run: it receives every log line exactly once and the current state, with no gap and no duplicate. (Tasks 7, 9)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/db/migrations/00006_runs.sql` | Runs, steps, legs, operations, locks, log lines, log archives |
| `internal/store/runs.go`, `legs.go`, `locks.go`, `loglines.go` | SQL for those tables |
| `internal/engine/plan.go` | Turn a job, an inventory and a selection into a run's steps, legs and lock resources |
| `internal/engine/decide.go` | Pure decision: given a run's rows, what happens next |
| `internal/engine/advance.go` | Apply decisions in one transaction; people's actions (cancel, approve, reconcile) |
| `internal/engine/loop.go` | Advance active runs on wake-ups and on a timer; lease sweep |
| `internal/queue/queue.go` | Claim, heartbeat, checkpoint, complete, release: all generation-checked |
| `internal/runner/runner.go`, `env.go` | Worker pool, executor registry, what an executor may do |
| `internal/steps/fake/fake.go` | Scripted executors for development and tests |
| `internal/logs/writer.go`, `reader.go`, `archive.go` | Batch writing, merged reading with cursors and gaps, archiving |
| `internal/wake/wake.go` | Valkey pub/sub wake-ups with polling fallback |
| `internal/api/runs.go`, `internal/api/events.go` | Run endpoints and the SSE stream |
| `test/e2e/engine_test.go` | Orchestrator and runners in-process with fake steps |

---

### Task 1: Tables and storage

**Files:**
- Create: `internal/db/migrations/00006_runs.sql`, `internal/store/runs.go`, `internal/store/legs.go`, `internal/store/locks.go`, `internal/store/loglines.go`
- Test: `internal/store/runs_integration_test.go`

**Interfaces:**
- Produces:

```go
type Run struct {
    ID                       int64
    Kind                     string // "deploy" | "restore"
    JobSourceID              int64
    JobID                    string
    JobSnapshot              []byte // JSON spec.Job
    InventoryCommit, JobCommit string
    Environment, Label, AWSAccount string
    FolderID                 int64
    Selection                []byte // JSON engine.Selection
    Manifest                 []byte // JSON, nil until resolved
    ManifestID               string
    ApprovalRequired         bool
    AutoStart                bool
    State, WaitReason        string
    RequestedBy              int64
    Via                      string // "web" | "cli"
    ParentRunID              *int64
    CancelRequestedBy        *int64
    CreatedAt                time.Time
    StartedAt, FinishedAt, CancelRequestedAt *time.Time
}
type RunStep struct{ ID, RunID int64; Name, Use, GroupID string; Position, MaxParallel int; Timeout time.Duration; Needs []byte /* JSON []spec.Need */ }
type Leg struct {
    ID, RunID    int64
    StepID       *int64 // nil for resolve and revalidate
    Kind         string
    Service, Version string
    State        string
    Attempt      int
    Generation   int64
    RunnerID     string
    LeaseUntil   *time.Time
    CancelRequested bool
    Resume, Result []byte // JSON
    FailureShort, FailureDetail string
    QueuedAt, StartedAt, FinishedAt *time.Time
}
type Op struct{ ID, LegID int64; Seq int; Name, Key string; Intent, Result []byte; State string; At time.Time } // State: "intended" | "done" | "failed"

func InsertRun(ctx, tx, r Run, steps []RunStep, legs []Leg) (Run, error) // one transaction; returns ids
func GetRun(ctx, db, id int64) (Run, error); func LockRun(ctx, tx, id int64) (Run, error) // SELECT ... FOR UPDATE
func ListRuns(ctx, db, f RunFilter) ([]Run, error) // RunFilter: JobSourceID, Environment, States, Query, BeforeID, Limit
func ActiveRunIDs(ctx, db) ([]int64, error)       // every run not in a final state
func UpdateRunState(ctx, tx, id int64, state, waitReason string, at time.Time) error
func SetManifest(ctx, tx, id int64, manifest []byte, manifestID string) error
func RequestCancel(ctx, tx, id, by int64, at time.Time) error
func StepsOfRun(ctx, db, runID int64) ([]RunStep, error); func LegsOfRun(ctx, db, runID int64) ([]Leg, error)
func SetLegState(ctx, tx, legID int64, state string, at time.Time) error        // orchestrator-side transitions: pending->queued/skipped/cancelled
func InsertLeg(ctx, tx, l Leg) (Leg, error)
func ExpiredLegs(ctx, db, now time.Time) ([]Leg, error)

func AcquireLocks(ctx, tx, runID int64, resources []string, at time.Time) (heldBy map[string]int64, err error) // empty map means acquired; otherwise nothing was taken
func ReleaseLocks(ctx, tx, runID int64) error
func LocksOfRun(ctx, db, runID int64) ([]string, error)
```

Behaviour:
- `AcquireLocks` sorts the resources, inserts them in one statement, and if any resource is already held by another run inserts nothing and reports who holds each contested one. Acquiring resources the same run already holds is a no-op.
- Tables carry check constraints for the state and kind lists in Global Constraints, a foreign key from legs to runs with cascade, an index for claiming (`state`, `queued_at`), and a unique lock per resource.
- `log_lines` has primary key (`run_id`, `leg_id`, `seq`), with `leg_id` 0 for lines the orchestrator writes about the run itself.

- [ ] **Step 1: Write the failing tests** (integration): `TestInsertRunCreatesStepsAndLegsAtomically` (a constraint violation in one leg leaves no run row); `TestLockRunBlocksSecondTransaction`; `TestAcquireLocksAllOrNothing` (run 1 holds `b`; run 2 asks for `a,b,c`: gets nothing, map names `b` held by 1; after release run 2 gets all three); `TestAcquireLocksIsIdempotentForSameRun`; `TestAcquireLocksConcurrentlyOneWinner` (two transactions, overlapping sets in opposite order, 50 rounds: exactly one holds everything each round and no deadlock error surfaces); `TestStateCheckConstraintsRejectUnknownValues`; `TestListRunsFiltersAndPages`; `TestExpiredLegsReturnsOnlyClaimedOrRunningPastLease`.
- [ ] **Step 2: Run** `make test-integration`; confirm failure for missing functions.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass; `db.LatestVersion()` is this migration's number.
- [ ] **Step 5: Commit** `feat: store runs, legs, operations, locks and log lines`.

---

### Task 2: Building a run from a selection

**Files:**
- Create: `internal/engine/plan.go`
- Test: `internal/engine/plan_test.go`

**Interfaces:**
- Consumes: `spec.Job`, `spec.Inventory`.
- Produces:

```go
type GroupSelection struct {
    Version  string
    Services map[string]string // service -> version override; "" means the group version. Absent means not selected
}
type Selection map[string]GroupSelection // group id -> selection

type Built struct {
    Steps     []store.RunStep
    Legs      []store.Leg   // one "resolve" leg in state queued, then one pending "step" leg per step per selected service
    Resources []string      // lock resources, sorted, unique
}
func Build(job spec.Job, inv spec.Inventory, envName string, sel Selection) (Built, spec.Problems)
```

Rules, each a problem:
- The environment exists and its label is in the job's `environment.labels`.
- Every selected group is a group of the job; every selected service belongs to that group and exists in that environment.
- At least one service is selected overall. Every selected service ends up with a non-empty version.
- A step whose group has no selected service gets no legs and is left out of `Steps`, and any `needs` pointing at it is dropped from the steps that remain.

Lock resources: for every selected service, its `ecs_service` ARN and every `destination_env_path` and `destination_toml_path` it has in that environment.

- [ ] **Step 1: Write the failing tests**: `TestBuildFullSelection` (2 groups of 4 and 3 services, 4 steps: 28 step legs plus 1 resolve leg; each leg carries its service and effective version; resources are 7 ARNs plus the destinations, sorted, no duplicates); `TestBuildPerServiceVersionOverride`; `TestBuildPartialSelectionDropsEmptyStepsAndTheirNeeds`; `TestBuildSkipsServiceNotInEnvironmentAsProblem`; `TestBuildRejections` table (unknown environment; label not permitted by the job; unknown group; service not in group; nothing selected; empty version).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test`. Expected: pass.
- [ ] **Step 5: Commit** `feat: build a run's steps, legs and lock resources from a selection`.

---

### Task 3: The decision function

**Files:**
- Create: `internal/engine/decide.go`
- Test: `internal/engine/decide_test.go`

**Interfaces:**
- Produces:

```go
type View struct { // everything Decide may look at; no database, no clock
    Run          store.Run
    Steps        []store.RunStep
    Legs         []store.Leg
    LocksHeld    bool
    Frozen       string // non-empty reason when a freeze blocks the environment (always "" until Plan 7)
    RunningInEnv, RunningInAccount int // legs of other runs currently queued, claimed or running
    Limits       Limits
}
type Limits struct{ PerEnvironment, PerAccount int }
type Decision struct {
    QueueLegs, SkipLegs, CancelLegs []int64
    MarkCancelRequested            []int64 // in-flight legs to flag
    CreateRevalidateLeg            bool
    TryAcquireLocks                bool
    NewState, WaitReason           string  // "" means unchanged
    ReleaseLocks                   bool
}
func Decide(v View) Decision
```

Behaviour by run state:
- `resolving`: resolve leg `succeeded` moves to `planned` unless `AutoStart`, in which case to `waiting_approval` if `ApprovalRequired`, else `queued`. Resolve leg `failed` moves to `failed`. `unknown` moves to `failed` with a reason (nothing was changed yet). A cancel request moves to `cancelled` and cancels the resolve leg if it has not been claimed, or flags it.
- `planned`: only a person's start moves it (Task 4), to `waiting_approval` if `ApprovalRequired`, else `queued`. A cancel request moves to `cancelled`.
- `waiting_approval`: only people's actions move it (Task 4). A cancel request moves to `cancelled`.
- `queued`: a cancel request moves to `cancelled`. A freeze sets the wait reason and waits. Otherwise `TryAcquireLocks`; Task 4 turns success into `running` with `CreateRevalidateLeg`, and failure into a wait reason naming the holding run.
- `running`, before the revalidate leg is settled: wait. Revalidate `failed` moves to `invalidated` and releases locks. `unknown` moves to `needs_reconciliation`.
- `running`, after revalidate `succeeded`: for each `pending` step leg decide ready, skip or wait from its step's `needs`:
  - Scope `step`: ready when every leg of the needed step is `succeeded`; skipped as soon as any leg of it is `failed`, `skipped` or `cancelled`.
  - Scope `service`: ready when the same service's leg in the needed step is `succeeded`; skipped when that leg is `failed`, `skipped` or `cancelled`.
  - No needs: ready.
  Ready legs are queued in order of step position then leg id, while respecting the step's `MaxParallel` (0 means no step limit) and both limits in `Limits`, counting this run's own queued, claimed and running legs.
- `running` with a cancel request: move to `cancelling`.
- `cancelling`: every `pending` and `queued` leg is cancelled; every `claimed` or `running` leg is flagged. Nothing is queued.
- `running` or `cancelling` with every leg settled: apply the result precedence. A final state sets `ReleaseLocks`; `needs_reconciliation` does not.
- `needs_reconciliation`: when no leg is `unknown` any more, apply the precedence and release locks.
- Final states: an empty decision.

- [ ] **Step 1: Write the failing tests**, each building a `View` by hand: one test per bullet above, plus:
  - `TestStepScopeSkipsWholeStepWhenOneNeededLegFails` and `TestServiceScopeSkipsOnlyThatService` on the same four-service fixture, asserting the exact leg ids queued and skipped. 
  - `TestSkipCascadesThroughChain` (a, then b needing a, then c needing b: a failed leg in a ends with b and c skipped across successive calls).
  - `TestMaxParallelAndLimitsCapQueueing` (step limit 2 with 5 ready legs queues 2; environment limit 10 with 9 already running elsewhere queues 1; account limit likewise).
  - `TestPrecedence` table over leg-state mixes: all succeeded; some failed; all failed; some skipped only; one unknown among successes; cancel requested with mixed settled legs; cancel requested with one unknown.
  - `TestCancelInEveryRunState` table. (Review Focus 4)
  - `TestDecideIsIdempotent`: applying a decision's leg changes to the view and calling again yields no further queue or skip for the same legs.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement** as a pure function: no I/O, no time.
- [ ] **Step 4: Run** `make lint test`. Expected: pass.
- [ ] **Step 5: Commit** `feat: decide a run's next transitions as a pure function`.

---

### Task 4: Advancing runs and people's actions

**Files:**
- Create: `internal/engine/advance.go`
- Test: `internal/engine/advance_integration_test.go`

**Interfaces:**
- Consumes: Tasks 1 to 3, `audit.Record`, `auth.Principal`.
- Produces:

```go
type Engine struct{ /* pool, limits source, clock, logger, wake publisher, run-log writer */ }
func New(deps Deps) *Engine
func (e *Engine) Create(ctx context.Context, p auth.Principal, jobSourceID int64, envName string, sel Selection, via string, autoStart bool) (store.Run, error)
func (e *Engine) Start(ctx context.Context, p auth.Principal, runID int64) error
func (e *Engine) Advance(ctx context.Context, runID int64) error
func (e *Engine) Cancel(ctx context.Context, p auth.Principal, runID int64) error
func (e *Engine) Approve(ctx context.Context, p auth.Principal, runID int64) error
func (e *Engine) Reject(ctx context.Context, p auth.Principal, runID int64) error
func (e *Engine) Expire(ctx context.Context, runID int64) error
func (e *Engine) Reconcile(ctx context.Context, p auth.Principal, runID, legID int64, outcome string) error // outcome: "succeeded" | "failed"
func (e *Engine) CheckAgain(ctx context.Context, p auth.Principal, runID, legID int64) error                 // requeue an unknown leg for one more reconciliation attempt
func (e *Engine) ReleaseLocks(ctx context.Context, p auth.Principal, runID int64) error                      // admins only
var ErrForbidden, ErrNotRunnable, ErrWrongState error
type SelectionError struct{ Problems spec.Problems }
```

Behaviour:
- `Create`: needs `run` on the target (label, environment, the job's folder). The job must be runnable and an inventory active (`ErrNotRunnable` otherwise). Builds with Task 2 (`*SelectionError` on problems), stores the run in `resolving` with the job snapshot and commits, sets `ApprovalRequired` from the job's `approval.when.labels`, audits, publishes a wake-up.
- `Advance`: in one transaction, lock the run row, load steps and legs, compute `View`, call `Decide`, apply it. For `TryAcquireLocks`: on success set `running`, create the revalidate leg queued; on failure stay `queued` with wait reason `waiting for run #<n>` naming the lowest holding run. Every transition writes one run-level log line (leg 0) saying what happened in plain words. Publishes a wake-up when it queued legs. Calling it on a final run does nothing.
- `Start`: needs `run` on the target; only from `planned` (`ErrWrongState` otherwise); audits; advances. `Expire` also moves a `planned` run older than 1 hour to `expired`.
- `Cancel`: needs `cancel` on the target; records who and when; allowed in any non-final state (`ErrWrongState` otherwise); then advances.
- `Approve` and `Reject`: need `approve` and pass `MayApproveRunStartedBy`; only from `waiting_approval`. Approve moves to `queued`; reject to `rejected`. `Expire` moves `waiting_approval` to `expired`.
- `Reconcile`: needs `restore` on the target; only for an `unknown` leg of a run in `needs_reconciliation`; sets the leg's state, audits with the person's choice, advances.
- `CheckAgain`: same permission; sets the leg back to `queued` keeping its resume data and operations; the run stays in `needs_reconciliation` until the leg settles.
- `ReleaseLocks`: admins only; allowed only in `needs_reconciliation`; releases, moves the run to `failed` with reason `locks released by <name> without reconciliation`, audits.

- [ ] **Step 1: Write the failing tests** (integration; legs are moved to settled states directly through the store to simulate runners):
  - `TestCreateStoresSnapshotAndStartsResolving`; `TestCreateNeedsRunPermissionOnTarget`; `TestCreateRefusesNotRunnableJob`.
  - `TestHappyPathStateSequence`: resolving, planned, (start), queued, running (revalidate), step legs queued by dependency, succeeded; locks held from `running` and released at the end; a run-level log line exists for each transition.
  - `TestPlannedRunWaitsForStartAndAutoStartDoesNot`; `TestPlannedRunExpiresAfterAnHour`; `TestSpecDocumentListsPlannedState` (a check, not a Go test: add `planned` to the state list in spec section 7.6 and a sentence in 7.3 saying the run waits in `planned` until started).
  - `TestApprovalRequiredWaitsAndHoldsNoLocks`; `TestApproveByStarterIsRefusedUnlessAdmin`; `TestRejectAndExpire`.
  - `TestSecondRunWaitsForFirstRunsLocks`: run B stays `queued` with reason `waiting for run #A` while A is `running` and while A is `needs_reconciliation`; it proceeds after A is final. (Review Focus 2)
  - `TestInterleavingFromTheDesignCannotHappen`: A and B share one service; drive A's config leg to success; assert no leg of B is queued; finish A; B then starts. (Review Focus 2)
  - `TestRevalidateFailureInvalidatesAndReleasesLocks`.
  - `TestTwoAdvancersNeverQueueALegTwice`: 8 goroutines call `Advance` in a loop while legs are settled; each leg's `queued_at` is set exactly once and the final state is correct.
  - `TestCancelWhileRunning`: pending legs cancelled, in-flight flagged, state `cancelling`; settling the in-flight legs gives `cancelled` and releases locks. (Review Focus 4)
  - `TestUnknownLegGivesNeedsReconciliationAndKeepsLocks`; `TestReconcileToSucceededFinishesRun`; `TestCheckAgainRequeuesLeg`; `TestReleaseLocksIsAdminOnlyAndAudited`.
  - `TestEveryPersonActionIsAudited`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: advance runs transactionally with locks, cancel and reconciliation`.

---

### Task 5: The queue between orchestrator and runner

**Files:**
- Create: `internal/queue/queue.go`
- Test: `internal/queue/queue_integration_test.go`

**Interfaces:**
- Produces:

```go
type Claimed struct{ Leg store.Leg; Run store.Run; Step *store.RunStep }
type Outcome struct{ State, FailureShort, FailureDetail string; Result []byte } // State: succeeded | failed | unknown | cancelled
type Queue struct{ /* pool, lease, clock */ }
func New(pool *pgxpool.Pool, lease time.Duration, now func() time.Time) *Queue
func (q *Queue) Claim(ctx context.Context, runnerID string) (*Claimed, error)            // nil, nil when nothing is queued
func (q *Queue) Heartbeat(ctx context.Context, legID, generation int64) error            // ErrLeaseLost
func (q *Queue) MarkRunning(ctx context.Context, legID, generation int64) error
func (q *Queue) Checkpoint(ctx context.Context, legID, generation int64, resume []byte) error
func (q *Queue) Intend(ctx context.Context, legID, generation int64, name, key string, intent []byte) (store.Op, error)
func (q *Queue) Finish(ctx context.Context, legID, generation, opID int64, ok bool, result []byte) error
func (q *Queue) Ops(ctx context.Context, legID int64) ([]store.Op, error)
func (q *Queue) Complete(ctx context.Context, legID, generation int64, o Outcome) error  // ErrLeaseLost
func (q *Queue) Release(ctx context.Context, legID, generation int64) error              // back to queued, attempt kept
func (q *Queue) CancelRequested(ctx context.Context, legID int64) (bool, error)
func (q *Queue) Sweep(ctx context.Context) (requeued, unknown int, err error)            // expired leases
var ErrLeaseLost error
```

Behaviour:
- `Claim` takes the oldest queued leg with `FOR UPDATE SKIP LOCKED`, sets it `claimed`, increments `generation` and `attempt`, sets the runner and lease.
- Every method that takes a generation changes the row only where the stored generation equals it and the state is `claimed` or `running`; zero rows changed is `ErrLeaseLost`.
- `Intend` records an operation as `intended` before the external change; calling it again with the same leg and key returns the existing operation instead of creating a second one.
- `Sweep` finds legs whose lease passed: with fewer than 5 attempts they go back to `queued` (resume data and operations kept); at 5 they become `unknown` with failure detail `no runner could confirm the outcome after 5 attempts`.

- [ ] **Step 1: Write the failing tests**:
  - `TestClaimOrderAndFields`; `TestClaimReturnsNilWhenEmpty`.
  - `TestConcurrentClaimsNeverShareALeg`: 50 goroutines claim from 200 queued legs; every leg is claimed exactly once.
  - `TestHeartbeatExtendsLease`; `TestStaleGenerationIsRejectedEverywhere` table over Heartbeat, MarkRunning, Checkpoint, Intend, Finish, Complete, Release after the leg was swept and reclaimed. (Review Focus 3)
  - `TestStaleCompleteDoesNotOverwriteNewerResult`: old holder calls `Complete(succeeded)` after the new holder completed `failed`; the leg stays `failed`. (Review Focus 3)
  - `TestIntendIsIdempotentPerKeyAndSurvivesReclaim`: intend, sweep, reclaim; `Ops` shows the `intended` operation and a second `Intend` with the same key returns it. (Review Focus 1)
  - `TestSweepRequeuesThenMarksUnknownAtFiveAttempts`.
  - `TestReleaseReturnsLegToQueueKeepingResume`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add the leased, generation-checked work queue`.

---

### Task 6: Runner, executors and fake steps

**Files:**
- Create: `internal/runner/runner.go`, `internal/runner/env.go`, `internal/steps/fake/fake.go`
- Modify: `internal/config/config.go` (`YUMDRAKE_ENV` default `prod`, values `dev` or `prod`; `YUMDRAKE_FAKE_STEPS` default `false`; `YUMDRAKE_RUNNER_CONCURRENCY` default 8; `YUMDRAKE_LEASE` default `30s`), `cmd/runner/main.go`
- Test: `internal/runner/runner_integration_test.go`, `internal/steps/fake/fake_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: `queue.Queue`, `logs.Writer` (Task 7; until it is merged, depend on the small interface below).
- Produces:

```go
type Env interface {
    Log(level, text string)                                              // level: info | ok | warn | error
    Resume(v any) (found bool, err error)                                // decode the last checkpoint
    Checkpoint(ctx context.Context, v any) error
    Ops(ctx context.Context) ([]store.Op, error)
    Do(ctx context.Context, name, key string, intent any, fn func() (result any, err error)) (store.Op, error)
    CancelRequested(ctx context.Context) bool
}
type Executor interface {
    Execute(ctx context.Context, env Env, task queue.Claimed) queue.Outcome
}
type LineSink interface{ Write(runID, legID int64, level, text string); Flush(ctx context.Context, legID int64) error }
type Runner struct{ /* unexported */ }
func New(q *queue.Queue, sink LineSink, id string, concurrency int, heartbeat, poll time.Duration, log *slog.Logger) *Runner
func (r *Runner) Register(kind string, e Executor) // kind: "resolve", "revalidate", "copy-config", "ecs-deploy"
func (r *Runner) Run(ctx context.Context, wake <-chan struct{}) error
```

Behaviour:
- `Run` claims while it has free slots, on every wake-up and every poll interval. Each leg runs in its own goroutine with a context that is cancelled if a heartbeat reports `ErrLeaseLost`. The executor kind is the leg kind for `resolve` and `revalidate`, and the step's `use` for step legs. A leg with no registered executor completes `failed` with a clear reason.
- `Env.Do` is the only way an executor makes an external change: if an operation with this key is already `done` it returns it without calling `fn`; if it is `intended` it returns it with `ErrAmbiguous` so the executor must reconcile against the real system; otherwise it records the intent, checks the lease is still held, calls `fn`, and records the result.
- When the executor returns, lines are flushed first, then `Complete` is called. If `Complete` reports `ErrLeaseLost` the result is dropped and logged locally only.
- An executor that panics is recovered: the leg completes `unknown` with the panic message (no stack trace in the stored reason).
- Shutdown: when the context ends, stop claiming, give running legs until the shutdown timeout, then `Release` what remains.
- At the start of an attempt after the first, the runner writes the line `attempt <n>: a previous attempt by runner <id> did not finish; continuing from its last checkpoint` at `warn` level.

Fake executors (`fake.Register(r *runner.Runner, opts fake.Options)`), driven by the leg's version string so a person can try every path from the UI, and by `Options` in tests:
- Version containing `fail`: the `ecs-deploy` leg fails with short reason `health checks failed`. Containing `unknown`: it returns `unknown`. Containing `slow`: it takes `Options.SlowFor`. Otherwise succeeds after `Options.StepTime`.
- `resolve` returns a small manifest listing the selection; `revalidate` succeeds, or fails when the environment name contains `stale`.
- Every fake writes several realistic log lines and uses `Env.Do` for its pretend change so recovery can be exercised.

- [ ] **Step 1: Write the failing tests** (integration, with a scripted test executor that blocks on channels):
  - `TestRunnerExecutesClaimedLegAndCompletes`; `TestRunnerHonoursConcurrencyLimit`; `TestUnregisteredKindFailsLegWithReason`.
  - `TestHeartbeatKeepsLeaseDuringLongLeg` (leg longer than three leases is not swept).
  - `TestLeaseLostCancelsExecutorAndDropsItsResult`. (Review Focus 3)
  - `TestDoSkipsFnWhenOperationAlreadyDone`; `TestDoReportsAmbiguousWhenOnlyIntended`; `TestCrashAfterChangeBeforeRecordIsReconciledByNextAttempt`: the first attempt's `fn` runs and the process "dies" (the test abandons that runner without completing); a second runner claims after a sweep; its executor sees `ErrAmbiguous`, consults the fake external system, and completes `succeeded` without running `fn` a second time. (Review Focus 1)
  - `TestPanicInExecutorCompletesUnknown`; `TestShutdownReleasesUnfinishedLegs`; `TestSecondAttemptWritesContinuationLine`.
  - `fake_test.go`: one test per version keyword.
  - `config_test.go`: `TestFakeStepsRefusedOutsideDev`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement**; wire the runner binary to start the worker pool, registering fakes only under the guard.
- [ ] **Step 4: Run** `make lint build test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add the runner worker pool, executor contract and fake steps`.

---

### Task 7: Logs

**Files:**
- Create: `internal/logs/writer.go`, `internal/logs/reader.go`, `internal/logs/archive.go`
- Modify: `internal/config/config.go` (`YUMDRAKE_LOG_ARCHIVE_DIR`, optional; when empty, archiving is off until Plan 5 adds S3)
- Test: `internal/logs/logs_integration_test.go`

**Interfaces:**
- Produces:

```go
type Line struct{ RunID, LegID, Seq int64; At time.Time; Level, Text string }
type Writer struct{ /* unexported */ }                          // satisfies runner.LineSink
func NewWriter(pool *pgxpool.Pool, flushEvery time.Duration, maxBatch int, now func() time.Time, notify func(runID int64)) *Writer
func (w *Writer) Write(runID, legID int64, level, text string)
func (w *Writer) Flush(ctx context.Context, legID int64) error
func (w *Writer) Close(ctx context.Context) error

type Filter struct{ StepID *int64; Service, Level, Query string }
type Cursor string                                              // opaque; encodes the last sequence seen per leg
type Page struct{ Lines []Line; Next Cursor; Gaps []Gap }
type Gap struct{ LegID, FromSeq, ToSeq int64 }
type Reader struct{ /* pool, archive */ }
func NewReader(pool *pgxpool.Pool, a Archive) *Reader
func (r *Reader) Read(ctx context.Context, runID int64, f Filter, after Cursor, limit int) (Page, error)

type Archive interface {
    Put(ctx context.Context, key string, data []byte) error
    Get(ctx context.Context, key string) ([]byte, error)
}
func NewDirArchive(dir string) (Archive, error)
type Archiver struct{ /* unexported */ }
func NewArchiver(pool *pgxpool.Pool, a Archive, olderThan time.Duration, now func() time.Time, log *slog.Logger) *Archiver
func (a *Archiver) ArchiveSettledLegs(ctx context.Context) (legs int, err error)
```

Behaviour:
- The writer numbers lines per leg, continuing after the highest sequence already stored for that leg. It flushes every `flushEvery` or `maxBatch` lines per leg, in order: a failed batch is retried before any later batch of the same leg is sent, so a gap can never come from the writer. Inserts ignore primary-key conflicts. Text longer than 16 KB is cut with a marker. `notify` is called after each committed batch.
- `Read` returns lines for the run ordered by time, then leg, then sequence, starting after the cursor, merging archived legs (read from the archive) with rows still in PostgreSQL. `Filter.Query` is a case-insensitive substring match. A missing sequence range inside a leg is reported in `Gaps` and never papered over. The cursor is safe against lines of different legs being committed out of time order: a later call never skips a line that was committed after the previous call.
- The archiver takes legs settled more than `olderThan` ago, writes each as gzip-compressed NDJSON under `runs/<run>/legs/<leg>.ndjson.gz`, records it in `log_archives`, and deletes the rows in the same transaction as the record, only after `Put` succeeded. Running it twice archives nothing twice.

- [ ] **Step 1: Write the failing tests**:
  - `TestWriterBatchesAndNumbersPerLeg`; `TestWriterContinuesNumberingAfterRestart`; `TestDuplicateBatchIsDropped`; `TestFailedBatchIsRetriedBeforeLaterOnes` (inject one failure; final sequences are contiguous); `TestLongLineIsCut`.
  - `TestReadOrdersAcrossLegsAndFilters` table (by step, service, level, text).
  - `TestCursorNeverSkipsLateCommittedLines`: leg A's batch with earlier timestamps is committed after leg B's later lines were already read; the next read returns A's lines. (Review Focus 5)
  - `TestCursorPagingReturnsEveryLineExactlyOnce` over 5,000 lines in pages of 200.
  - `TestGapIsReported` (delete a middle row by hand).
  - `TestArchiveThenReadIsIdenticalToBefore`; `TestArchiveFailureKeepsRows`; `TestArchiverIsIdempotent`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add acknowledged log writing, merged reading and archiving`.

---

### Task 8: Wake-ups and the engine loop

**Files:**
- Create: `internal/wake/wake.go`, `internal/engine/loop.go`
- Modify: `cmd/orchestrator/main.go`, `cmd/runner/main.go`
- Test: `internal/wake/wake_integration_test.go`, `internal/engine/loop_integration_test.go`

**Interfaces:**
- Produces:

```go
// wake
type Bus struct{ /* valkey client */ }
func New(c valkey.Client, log *slog.Logger) *Bus
func (b *Bus) Publish(ctx context.Context, topic string, id int64) // best effort; never returns an error to the caller
func (b *Bus) Subscribe(ctx context.Context, topic string) <-chan int64 // reconnects on its own; closed when ctx ends
const TopicLegs, TopicRuns, TopicLogs = "legs", "runs", "logs"

// engine
func (e *Engine) Loop(ctx context.Context, wakeRuns <-chan int64, sweepEvery time.Duration, q *queue.Queue, expireApprovalsAfter func(store.Run) time.Duration)
```

Behaviour:
- `Loop` advances a run when its id arrives on the channel, and on every `sweepEvery` tick it runs `q.Sweep`, expires approvals past their time, and advances every active run. A failure for one run is logged and does not stop the others.
- With Valkey down, publishing is a no-op and subscribers receive nothing; the system still makes progress on the timers.
- The runner publishes on `TopicRuns` after completing a leg; the orchestrator publishes on `TopicLegs` after queuing; the log writer's `notify` publishes on `TopicLogs`.

- [ ] **Step 1: Write the failing tests**: `TestPublishReachesSubscriber`; `TestSubscriberSurvivesValkeyRestart` (stop and start the Compose Valkey service is not allowed in tests; instead point the bus at a small TCP proxy the test can cut and restore); `TestLoopAdvancesOnWake`; `TestLoopMakesProgressWithoutValkey` (bus pointed at a closed port; a run still reaches its final state through the timer); `TestLoopSweepsExpiredLeasesAndExpiresApprovals`; `TestOneFailingRunDoesNotBlockOthers`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement** and wire both binaries.
- [ ] **Step 4: Run** `make lint build test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: drive runs with wake-ups and a fallback timer`.

---

### Task 9: Run API and the event stream

**Files:**
- Create: `internal/api/runs.go`, `internal/api/events.go`
- Test: `internal/api/runs_integration_test.go`, `internal/api/events_integration_test.go`

Routes:

| Method and path | Needs | Does |
|---|---|---|
| `POST /api/v1/jobs/{id}/runs` | `run` on the target | Body: environment, selection and `start` (boolean, default false). Creates the run, which plans itself. 422 with the problems for a bad selection |
| `POST /api/v1/runs/{id}/start` | `run` on the target | Starts a `planned` run |
| `GET /api/v1/runs` | `view` | Filters: `job`, `environment`, `state` (repeatable), `q`, `before`, `limit` (at most 100) |
| `GET /api/v1/runs/{id}` | `view` | The run, its steps and legs with states, versions, attempts and failure reasons, its wait reason, the locks it holds, and a summary: counts by leg state and the list of failures (service, step, short and full reason) |
| `GET /api/v1/runs/{id}/log` | `view_logs` on the target | `step`, `service`, `level`, `q`, `after`, `limit` (at most 1000). Returns lines, the next cursor, and gaps |
| `GET /api/v1/runs/{id}/events` | `view`; log events also need `view_logs` | Server-Sent Events |
| `POST /api/v1/runs/{id}/cancel` | `cancel` | |
| `POST /api/v1/runs/{id}/legs/{leg}/reconcile` | `restore` | Body: `outcome` |
| `POST /api/v1/runs/{id}/legs/{leg}/check-again` | `restore` | |
| `POST /api/v1/admin/runs/{id}/release-locks` | admin | |

Event stream:
- Event `state`: the same body as `GET /runs/{id}`'s states and summary, sent on connect and after every change.
- Event `log`: a batch of lines; its `id` is the cursor after that batch.
- A client reconnecting with `Last-Event-ID` (or `?after=`) gets a `state` event first, then log lines after that cursor. No line is sent twice and none is missed. (Review Focus 5)
- A comment line is sent every 15 seconds as a keep-alive. The stream ends with an `end` event shortly after the run reaches a final state and the last lines were sent.
- Without `view_logs`, the stream carries `state` events only.
- The handler stops promptly when the client disconnects, and sets headers that stop proxies buffering it.

- [ ] **Step 1: Write the failing tests**: `TestCreateRunValidatesSelectionWith422`; `TestCreateRunNeedsPermission`; `TestGetRunIncludesFailureSummary`; `TestListRunsFilters`; `TestLogEndpointFiltersPagesAndNeedsViewLogs`; `TestCancelReconcileCheckAgainReleaseLocksPermissionsAndStates` table; `TestEventsSendStateThenLogsThenEnd`; `TestEventsResumeWithLastEventIDHasNoGapAndNoDuplicate` (read 3 batches, drop the connection, write more lines, reconnect with the last id, collect to the end: the union is every sequence exactly once); `TestEventsWithoutViewLogsCarryNoLogEvents`; `TestEventsHandlerStopsOnClientDisconnect` (no goroutine left after 1s).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add run endpoints and the live event stream`.

---

### Task 10: End-to-end check with fake steps

**Files:**
- Create: `test/e2e/engine_test.go`
- Modify: `compose.yaml` and `scripts/smoke.sh` (run the runner with fake steps under `YUMDRAKE_ENV=dev` in the smoke stack only), `README.md`

Each test starts one orchestrator and two runners in-process against the Compose PostgreSQL and Valkey, with a fake GitHub serving the fixture inventory and job, and signs in as an admin.

- [ ] **Step 1: Write the failing tests**:
  - `TestRunSucceedsEndToEnd`: create a run over two groups; it reaches `succeeded`; every leg succeeded; the log has lines for every leg and for each run-level transition; locks are gone.
  - `TestOneServiceFailsOthersContinue`: one service's version contains `fail`; with a `service`-scoped dependency only that service's later legs are skipped; the run is `partially_succeeded`; the summary names the service, the step and `health checks failed`.
  - `TestStepScopedBarrierSkipsWholeStep`: the same with a `step`-scoped dependency.
  - `TestRunnerKilledMidLegIsResumedByTheOther`: stop one runner's context abruptly while it holds a `slow` leg (no release); after the lease passes the other runner finishes it; the leg has attempt 2 and the continuation line; the pretend external change happened exactly once. (Review Focus 1)
  - `TestSecondRunWaitsThenProceeds`. (Review Focus 2)
  - `TestCancelMidRun`: final state `cancelled`; no leg started after the cancel; legs that had finished stay succeeded. (Review Focus 4)
  - `TestUnknownOutcomeNeedsReconciliationThenFinishes`: a version containing `unknown`; the run sits in `needs_reconciliation` holding its locks; reconcile to `succeeded`; the run finishes and releases them.
  - `TestStreamFollowsRunAndSurvivesReconnect`. (Review Focus 5)
  - `TestTwoOrchestratorsProduceOneConsistentRun`: two orchestrators advancing the same runs; no leg is executed twice.
- [ ] **Step 2: Run** `make test-e2e`; confirm failure.
- [ ] **Step 3: Implement** the tests and the smoke changes.
- [ ] **Step 4: Run** `make lint test test-integration test-e2e smoke`. Expected: all pass.
- [ ] **Step 5: Commit** `test: prove the run engine end to end with fake steps`.
- [ ] **Step 6: Push, open a pull request against the previous plan's branch, confirm CI passes.** Do not merge it.

---

## Parallel execution

Tasks 1 and 2 together. Then Tasks 3, 5 and 7 together (3 needs only the types from 1; 5 and 7 need 1). Then Tasks 4 and 6 together. Then Task 8, then 9, then 10.

## Definition of done

- `make lint test test-integration test-e2e smoke` passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- No new third-party module.
- A reviewer confirms the five Review Focus conditions from the named tests.
