# Yumdrake Plan 9: Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** People can see how deployments are going and what is running where: an overview with analytics, a versions matrix, a queue view, a page per service with its history, and a system health page for admins.

**Architecture:** Read-only endpoints over data the earlier plans already store, plus one new piece of state: runners report themselves so capacity and health are known. Running versions are read from ECS through the discovery credentials and cached. The web screens are built from the existing component kit.

**Tech Stack:** As before. No new Go module and no new web dependency. Charts are hand-built from HTML and CSS as in the mockup.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 12 and 15 metrics list). The mockup `docs/mockup/yumdrake-mockup.dc.html` is the reference for Overview and Versions. Queue, the service page and System health are not in the mockup: build them from the kit as specified here.

**Builds on:** Plans 1 to 8b.

No full code, by the owner's rule.

## Global Constraints

- All earlier Global Constraints apply, backend and web. No new dependency on either side.
- Every endpoint here is read-only and never calls AWS with deployment credentials.
- Analytics count only what the caller may view. With `view` open to every signed-in person in version 1, that is everything; the queries still take the principal so scoping can be added without changing callers.
- A number shown to a person always says what it compares to or what period it covers.
- Charts: one accent per chart, the two series differ in lightness as well as hue (succeeded `#5B8DC9`, failed `#D03A26`), a legend, a text readout on hover and focus, and a table alternative for assistive technology.
- An ECS read that fails for one service or environment shows as "could not read" in that cell with the AWS error code, and never fails the whole page.
- Running-version data is cached in Valkey for 2 minutes. With Valkey down the page still works, more slowly.

## Review Focus

1. No runs yet, or none in the chosen period or label: every tile, chart and list has a real empty state, and no division by zero shows `NaN%`. (Tasks 2, 5)
2. One AWS account is unreachable: the versions matrix still renders every other environment and marks the unreachable cells. (Tasks 3, 6)
3. A service was deployed outside Yumdrake: its cell shows the version read from ECS and "not deployed by Yumdrake", not a blank or a stale Yumdrake value. (Task 3)
4. All runners are down: the queue and health pages say so plainly ("No runner has reported for 2 minutes"), and queued runs show how long they have waited. (Tasks 1, 4, 7)
5. Thirty-three services across five environments: the matrix loads in one request to Yumdrake and stays readable on a narrow screen by scrolling inside its card with the service column fixed. (Tasks 3, 6)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/db/migrations/<next>_runners.sql`, `internal/store/runners.go` | Runner registry and counters |
| `internal/runner/heartbeat.go` | A runner reporting itself |
| `internal/analytics/overview.go` | Overview queries |
| `internal/running/running.go` | Running versions from ECS, cached |
| `internal/api/overview.go`, `versions.go` (extended), `queue.go`, `services.go`, `health.go` | Endpoints |
| `web/src/features/overview/*`, `versions/*`, `queue/*`, `service/*`, `health/*` | Screens |
| `web/src/ui/BarChart.tsx`, `RankList.tsx`, `StatTile.tsx` | Chart pieces |
| `test/e2e/visibility_floci_test.go`, `web/e2e/visibility.spec.ts` | End to end |

---

### Task 1: Runner registry and system counters

**Files:** the migration, `internal/store/runners.go`, `internal/runner/heartbeat.go`; modify `internal/runner/runner.go`, `internal/queue/queue.go` (count expired leases)
**Test:** `internal/store/runners_integration_test.go`, `internal/runner/heartbeat_integration_test.go`

**Interfaces:**

```go
type RunnerInfo struct{ ID, Version string; Concurrency, Busy int; StartedAt, LastSeenAt time.Time }
func UpsertRunner(ctx, db, r RunnerInfo) error
func ListRunners(ctx, db, seenSince time.Time) ([]RunnerInfo, error)
func RecordEvent(ctx, db, kind string, at time.Time) error            // kinds: lease_expired, log_persist_failed, sync_failed
func CountEvents(ctx, db, kind string, since time.Time) (int, error)
func QueueStats(ctx, db, now time.Time) (queuedLegs int, oldestQueued *time.Time, err error)
```

- A runner upserts itself every 10 seconds with how many legs it is running, and once more on clean shutdown with `Busy` 0. A runner not seen for 2 minutes is considered gone. Rows older than 7 days are deleted by the engine loop.
- `Sweep` records one `lease_expired` event per requeued or unknown leg. The log writer records `log_persist_failed` when a batch fails after retries. The sync engine records `sync_failed`.

- [ ] **Step 1: Write the failing tests**: `TestUpsertRunnerAndListSeenSince`; `TestRunnerReportsBusyCount`; `TestGoneRunnerDropsOutAfterTwoMinutes`; `TestSweepRecordsLeaseExpired`; `TestQueueStatsOldestQueued`; `TestOldRunnerRowsArePruned`.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint test test-integration`; expect pass.
- [ ] **Step 5: Commit** `feat: record runners and system counters`.

---

### Task 2: Overview analytics

**Files:** `internal/analytics/overview.go`, `internal/api/overview.go`
**Test:** `internal/analytics/overview_integration_test.go`, `internal/api/overview_integration_test.go`

**Interfaces:**

```go
type Overview struct {
    From, To          time.Time
    Runs, PrevRuns    int
    SuccessRate, PrevSuccessRate *float64 // nil when no finished run; 0..1
    MedianDuration, PrevMedianDuration *time.Duration
    WaitingApproval   int
    OldestWaiting     *time.Time
    Days              []Day            // one per day in the period, oldest first, zeros included
    TopFailing        []ServiceFailures // at most 5
    Attention         []AttentionItem
    Environments      []EnvLast
}
type Day struct{ Date string; Succeeded, Failed, Other int } // Failed includes partially_succeeded; Other: cancelled, invalidated
type ServiceFailures struct{ Service string; Failures int; CommonReason string }
type AttentionItem struct{ RunID int64; State, Job, Environment, Label, Reason, By string; At time.Time }
type EnvLast struct{ Environment, Label string; RunID *int64; State, Job string; At *time.Time }
func Load(ctx context.Context, db store.DBTX, label string, days int, now time.Time) (Overview, error)
```

- Period: the last `days` whole days ending today (default 14, allowed 7, 14, 30), in UTC. "Previous" is the same length immediately before.
- Runs counted: kind `deploy` or `restore` that left `planned`. Success rate: `succeeded` over all finished (`succeeded`, `partially_succeeded`, `failed`). Median duration: started to finished of finished runs.
- Top failing: failed `ecs-deploy` and `copy-config` legs grouped by service, with the most frequent short reason.
- Attention: runs in `waiting_approval` and `needs_reconciliation`, and each job-and-environment whose most recent finished run is `failed` or `partially_succeeded`.
- Route `GET /api/v1/analytics/overview?label=&days=` (`view`).

- [ ] **Step 1: Write the failing tests** over a seeded set of runs with fixed times: `TestTotalsAndPreviousPeriod`; `TestSuccessRateNilWithNoFinishedRuns` (Review Focus 1); `TestMedianDurationOddAndEvenCounts`; `TestDaysIncludeZeroDays`; `TestLabelFilter`; `TestTopFailingWithCommonReason`; `TestAttentionExcludesFailuresFixedByALaterRun`; `TestEnvironmentsLastRun` including an environment with none; endpoint validation of `days` and `label`.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement** with SQL aggregates, not by loading runs into memory. **Step 4: Run** `make lint test test-integration`; expect pass.
- [ ] **Step 5: Commit** `feat: add overview analytics`.

---

### Task 3: Running versions

**Files:** `internal/running/running.go`; extend `internal/api/versions.go`
**Test:** `internal/running/running_test.go`, `internal/running/running_floci_test.go`, `internal/api/running_integration_test.go`

**Interfaces:**

```go
type Cell struct {
    State          string // "ok" | "absent" (service not in this environment) | "error"
    Version        string // from the task definition's yumdrake:version tag; else a tag found for the image digest; else the image reference's tag; else ""
    TaskDefinition string // family:revision
    ByYumdrake     bool
    RunID          int64  // the run that applied it, when ByYumdrake
    LastDeployFailed bool // the newest ecs-deploy leg for this service and environment failed
    Behind         bool   // an environment with the same label runs a newer version by the service's rule
    Error          string // AWS error code, when State is "error"
    ReadAt         time.Time
}
type Matrix struct{ Environments []spec.Environment; Rows []Row }
type Row struct{ Service string; Cells []Cell } // cells in environment order
type Service struct{ /* discovery provider, cache, store */ }
func (s *Service) Matrix(ctx context.Context, inv spec.Inventory) (Matrix, error)
func (s *Service) ForService(ctx context.Context, inv spec.Inventory, name string) ([]Cell, error)
```

- Reads `DescribeServices` in batches of 10 per cluster and `DescribeTaskDefinition` with tags, at most 8 calls at a time, per environment account through the discovery credentials. Each environment's result is cached for 2 minutes; a failed environment is cached for 20 seconds.
- `Behind` is computed with `versions.CompareSemver` (or push order for `pushed` rules) among cells of the same label.
- Route `GET /api/v1/versions/running` (`view`) returns the matrix; `?refresh=1` bypasses the cache for people with `manage_connections`.

- [ ] **Step 1: Write the failing tests**: `TestVersionFromTagThenDigestThenImage`; `TestDeployedOutsideYumdrakeIsMarked` (Review Focus 3); `TestBehindWithinLabelOnly`; `TestLastDeployFailedFromNewestLeg`; `TestOneEnvironmentFailingMarksOnlyItsCells` (Review Focus 2); `TestAbsentService`; `TestBatchesOfTenAndBoundedConcurrency` with a counting stub; `TestCacheTwoMinutesAndWorksWithoutValkey`; Floci: `TestMatrixAgainstFloci` after a real deploy from Plan 6's setup; endpoint shape and `refresh` permission.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint test test-integration test-floci`; expect pass.
- [ ] **Step 5: Commit** `feat: read running versions from ECS`.

---

### Task 4: Queue, service and health endpoints

**Files:** `internal/api/queue.go`, `services.go`, `health.go`
**Test:** `internal/api/visibility_integration_test.go`

| Method and path | Needs | Returns |
|---|---|---|
| `GET /api/v1/queue` | `view` | Runs in `queued`, `running`, `cancelling`, `needs_reconciliation` and `waiting_approval`, oldest first, each with state, wait reason, how long it has waited, legs running and queued; every held lock with its run; runners seen in the last 2 minutes with busy and concurrency; total free slots; queued legs and the age of the oldest |
| `GET /api/v1/services/{name}` | `view` | The inventory entry; running cells per environment (Task 3); deployment history: the last 50 `ecs-deploy` legs for this service with run, environment, version, state, when and who; config history: per run the config item names and checksums (first 12 characters); and per environment the newest run a restore could start from, if any |
| `GET /api/v1/system/health` | `manage_connections` or admin | Runners; queue stats; counts in the last hour and day of `lease_expired`, `log_persist_failed`, `sync_failed`; sync age and state of the inventory and each job; each AWS account, GitHub connection and Slack with when it last worked and its last error code; notification backlog (pending, failed, oldest pending); and a list of plain-words problems derived from all of it, empty when healthy |

Problems list rules: no runner seen for 2 minutes; oldest queued leg older than 5 minutes; inventory not checked for 10 minutes; any `log_persist_failed` in the last hour; more than 5 `lease_expired` in the last hour; any failed notification in the last day; any connection whose last attempt failed.

- [ ] **Step 1: Write the failing tests**: queue lists waiting runs with reasons and durations, locks and holders, runner capacity; `TestQueueSaysWhenNoRunnerHasReported` (Review Focus 4); service page history order and restore source; `TestHealthProblems` table, one row per rule, plus the healthy case; permissions.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint test test-integration`; expect pass.
- [ ] **Step 5: Commit** `feat: add queue, service and system health endpoints`.

---

### Task 5: Overview screen

**Files:** `web/src/ui/StatTile.tsx`, `BarChart.tsx`, `RankList.tsx`, `web/src/features/overview/OverviewPage.tsx`
**Test:** one test file per component

As the mockup's Overview: a label filter (`Segmented`: All environments, dev, preprod, prod) and a period select (7, 14, 30 days), both in the URL. Four `StatTile`s: Runs, Success rate, Median duration, Waiting for approval, each with its comparison line ("+18 vs the previous 14 days", "3 points lower than the previous 14 days", "Oldest requested 2 hours ago", "Nothing is waiting"). "Runs per day" as a stacked `BarChart` with the readout line, legend and a visually hidden table of the same numbers. "Services that failed most" as a `RankList`. "Needs attention" as a list of links to runs. "Environments" as a table of last run per environment.

- `BarChart` props: `days: {label, title, values: {key, value}[]}[]`, `series: {key, label, color}[]`, `height`. Each day is a focusable button; hover or focus sets the readout. A zero day has no bar. Y axis ticks are whole numbers.
- A tile with nothing to show says "No runs in this period" instead of a number, never `NaN` or `0%`. (Review Focus 1)

- [ ] **Step 1: Write the failing tests**: tiles with comparisons in each direction and the empty case; chart readout on focus and the hidden table matching the data; zero-run period shows empty states everywhere (Review Focus 1); filters change the request and the URL; attention items link to their runs; loading and error.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make web-lint web-test`; expect pass.
- [ ] **Step 5: Commit** `feat(web): add the Overview screen`.

---

### Task 6: Versions matrix and the service page

**Files:** `web/src/features/versions/VersionsPage.tsx`, `web/src/features/service/ServicePage.tsx`; link service names to their page from Inventory, the matrix, `WhatFailed` and the steps tree
**Test:** one test file per screen

- Versions, as the mockup: search, a "Only differences" toggle, a legend (behind other environments with the same label; last deploy failed; not deployed by Yumdrake), a table with one row per service and one column per environment. A cell shows the version in mono with its marker and, as a tooltip and in a row detail for keyboard and touch, the task definition and when it was read. `error` cells show "could not read" and the code. `absent` cells show "not set up". The first column stays fixed while the rest scrolls sideways. (Review Focus 5)
- Service page (`/services/:name`): the service's facts; a card per environment with the running version, its markers, and "Restore" when a restore source exists and the person has `restore` (goes through the same dialog as on a run); deployment history as a table linking to runs; config history as a table of run, file and checksum; and its inventory block as in the Inventory screen.

- [ ] **Step 1: Write the failing tests**: matrix cells for each state and marker; search and differences-only; error cells for one environment while others render (Review Focus 2); "not deployed by Yumdrake" marker (Review Focus 3); the service column has the sticky style and the table scrolls inside its card; service page sections, restore visibility by permission, links to runs.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make web-lint web-test`; expect pass.
- [ ] **Step 5: Commit** `feat(web): add the versions matrix and the service page`.

---

### Task 7: Queue and system health screens

**Files:** `web/src/features/queue/QueuePage.tsx`, `web/src/features/health/HealthPage.tsx`; add "Queue" to the sidebar under Runs, and "System health" as an Admin section
**Test:** one test file per screen

- Queue: refreshed every 5 seconds. A line of capacity ("2 runners, 5 of 16 slots busy", or the no-runner warning from Review Focus 4). A table of active and waiting runs: run, job, environment, state, what it waits for in words with a link to the blocking run, how long. A table of locks: resource (mono, shortened in the middle with the full value as a tooltip), held by which run, since when.
- System health: the problems list first, as error notes, or "Everything is working" when empty. Then sections: Runners, Queue, Sync (inventory and each job with age and state), Connections (AWS accounts, GitHub, Slack), Notifications, Recent failures (the three counters for the last hour and day).

- [ ] **Step 1: Write the failing tests**: capacity line and the no-runner warning; waiting reasons link to the blocking run; lock table; health problems render in order and the healthy message; each section's rows; health hidden without permission; both pages keep working when a refresh fails (they show the last data and a small "could not refresh" note).
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make web-lint web-test web-build`; expect pass.
- [ ] **Step 5: Commit** `feat(web): add the Queue and System health screens`.

---

### Task 8: End-to-end check

**Files:** `test/e2e/visibility_floci_test.go`, `web/e2e/visibility.spec.ts`

- [ ] **Step 1: Write the failing tests**. Go: after two real deployments on Floci (one clean, one with a failing service), the overview totals, top failing and attention match; the matrix shows the deployed versions, marks the failed service, and marks a service changed by hand as not deployed by Yumdrake; stopping the runner makes the queue and health report it within 2 minutes (shortened by configuration in the test). Browser: Overview shows the runs; Versions shows the matrix and filtering; a service page lists its history and offers Restore; Queue shows a waiting run and its reason; System health lists a problem when the runner is stopped.
- [ ] **Step 2: Run** `make test-e2e web-e2e`; confirm failure. **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint build test test-integration test-floci test-e2e web-lint web-test web-e2e smoke floci-clean`; expect all pass.
- [ ] **Step 5: Commit** `test: prove the visibility screens end to end`.
- [ ] **Step 6: Push, open a pull request, confirm CI passes.** Do not merge it.

---

## Parallel execution

Tasks 1, 2 and 3 together. Then Task 4, with Tasks 5 and 6 starting at the same time against mocked routes. Then Task 7. Then Task 8.

## Definition of done

- The command in Task 8 Step 4 passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- No new dependency.
- A reviewer confirms the five Review Focus conditions from the named tests.
