# Yumdrake Plan 7: Approvals, Freezes and Slack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A job that asks for approval waits for a second person who approves exactly the plan they saw; an admin can freeze an environment or a label; and Slack hears about requests, decisions, starts and results without ever affecting a run.

**Architecture:** Approvals and freezes are small tables consulted by the engine's existing transitions. Notifications are written to an outbox table in the same transaction as the transition that causes them and delivered by a retrying sender, so a Slack outage delays messages and nothing else.

**Tech Stack:** Go 1.26, PostgreSQL, Slack Web API through `net/http`. No new third-party module.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 7.4, 7.7 last bullet, 9.3, 10, 19 decision 2)

**Builds on:** Plans 1 to 6. Use the actual names in the code where they differ.

No full code, by the owner's rule.

## Global Constraints

- All earlier plans' Global Constraints still apply. No new third-party module.
- Approval is required only where the job's YAML says so. There are no admin-set policies.
- Nobody approves a run they started, unless they are a global admin.
- An approval is bound to the manifest identity the approver was shown. Approving with a different identity is refused.
- A waiting run expires after the job's `approval.expires` (default 24 hours).
- A freeze blocks runs from starting to execute in its scope. Runs already executing continue. There is no bypass: lifting the freeze is the audited action.
- A notification failure never changes a run's state, never blocks a transition and never surfaces to the person as an error on the run.
- At most one notification of each kind per run.
- The Slack token is stored encrypted with purpose `slack`, never returned, never logged.
- Every approval, rejection, freeze and lift is audited in the same transaction.

## Review Focus

1. The plan changes between the approver opening the page and pressing Approve (the run was planned again): the approval is refused with a message to reload, and nothing is approved. (Task 2)
2. Two approvers press Approve and Reject at the same moment: exactly one decision is recorded and the other gets a clear "already decided". (Task 2)
3. A freeze is created while a run is queued behind another run's locks: when the locks free up, the run still does not start, and it says why. It starts by itself when the freeze is lifted or expires. (Task 3)
4. Slack is down for an hour: every run behaves exactly as with Slack up, and the messages arrive, in order per run, when it returns. (Tasks 4, 5, 6)
5. The Slack channel in a job's YAML does not exist or the bot is not in it: the notification is marked failed with Slack's error after its retries, visible in system health data, and the run is untouched. (Task 5)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/db/migrations/<next>_governance.sql` | Approvals, freezes, notifications |
| `internal/store/approvals.go`, `freezes.go`, `notifications.go` | SQL |
| `internal/engine/approval.go` | Request, decide and expire approvals inside engine transitions |
| `internal/engine/freeze.go` | Find the freeze that applies to a run |
| `internal/notify/message.go` | Turn a run event into a message |
| `internal/notify/outbox.go` | Enqueue in a transaction; deliver with retries |
| `internal/notify/slack.go` | Slack Web API client |
| `internal/api/approvals.go`, `freezes.go`, `slack.go` | Endpoints |
| `test/e2e/governance_test.go` | End to end with a fake Slack |

---

### Task 1: Tables and storage

**Files:**
- Create: the migration, `internal/store/approvals.go`, `freezes.go`, `notifications.go`
- Test: `internal/store/governance_integration_test.go`

**Interfaces:**
- Produces:

```go
type Approval struct{ ID, RunID int64; ManifestID string; RequestedBy int64; RequestedAt, ExpiresAt time.Time; DecidedBy *int64; DecidedAt *time.Time; Decision, Comment string } // Decision: "" | "approved" | "rejected" | "expired"
func RequestApproval(ctx, tx, a Approval) (Approval, error)                       // one open approval per run
func OpenApproval(ctx, db, runID int64) (Approval, bool, error)
func DecideApproval(ctx, tx, id int64, decision string, by *int64, comment string, at time.Time) error // ErrAlreadyDecided when it is no longer open
func OpenApprovals(ctx, db) ([]Approval, error); func ExpiredApprovals(ctx, db, now time.Time) ([]Approval, error)

type Freeze struct{ ID int64; Label, Environment, Reason string; CreatedBy int64; CreatedAt, ExpiresAt time.Time; LiftedBy *int64; LiftedAt *time.Time } // exactly one of Label, Environment
func CreateFreeze(ctx, tx, f Freeze) (Freeze, error); func LiftFreeze(ctx, tx, id, by int64, at time.Time) error // ErrNotFound if not active
func ActiveFreezes(ctx, db, now time.Time) ([]Freeze, error); func ListFreezes(ctx, db, limit int) ([]Freeze, error)

type Notification struct{ ID, RunID int64; Kind, Channel string; Payload []byte; State string; Attempts int; NextAttemptAt time.Time; LastError string; CreatedAt time.Time; SentAt *time.Time } // State: pending | sent | failed
func EnqueueNotification(ctx, tx, n Notification) error                           // silently does nothing if (run, kind) already exists
func ClaimDueNotifications(ctx, db, now time.Time, limit int) ([]Notification, error) // FOR UPDATE SKIP LOCKED, oldest first, never a later one of a run while an earlier one of that run is pending
func MarkNotificationSent(ctx, db, id int64, at time.Time) error
func MarkNotificationRetry(ctx, db, id int64, lastError string, next time.Time, failed bool) error
func NotificationStats(ctx, db) (pending, failed int, oldestPending *time.Time, err error)
var ErrAlreadyDecided error
```

- [ ] **Step 1: Write the failing tests**: `TestOneOpenApprovalPerRun`; `TestDecideApprovalOnce` (second decision is `ErrAlreadyDecided`); `TestDecideApprovalConcurrentlyOneWins` (two transactions; Review Focus 2); `TestExpiredApprovals`; `TestFreezeNeedsExactlyOneScope`; `TestActiveFreezesExcludeLiftedAndExpired`; `TestEnqueueIsOncePerRunAndKind`; `TestClaimKeepsPerRunOrder` (a run's second message is not claimed while its first is pending or retrying); `TestNotificationStats`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: store approvals, freezes and the notification outbox`.

---

### Task 2: Approvals

**Files:**
- Create: `internal/engine/approval.go`, `internal/api/approvals.go`
- Modify: `internal/engine/advance.go` (Plan 4's `Approve`, `Reject`, `Expire` now go through the approvals table)
- Test: `internal/engine/approval_integration_test.go`, `internal/api/approvals_integration_test.go`

**Interfaces:**
- Changes Plan 4's signatures to:

```go
func (e *Engine) Approve(ctx context.Context, p auth.Principal, runID int64, manifestID, comment string) error
func (e *Engine) Reject(ctx context.Context, p auth.Principal, runID int64, comment string) error
var ErrManifestChanged error
```

Behaviour:
- When a run enters `waiting_approval`, an approval is requested in the same transaction with the run's manifest identity and an expiry of now plus the job's `approval.expires`.
- `Approve`: needs `approve` on the target and must pass `MayApproveRunStartedBy`; `manifestID` must equal the run's (`ErrManifestChanged` otherwise, Review Focus 1); records the decision; moves the run to `queued`. A concurrent decision gives `ErrAlreadyDecided`.
- `Reject`: same permission rule; moves the run to `rejected`.
- The engine loop's expiry uses `ExpiredApprovals`.

Routes:

| Method and path | Needs | Does |
|---|---|---|
| `GET /api/v1/approvals` | `view` | Runs waiting for approval, each with who asked, when, when it expires, the job, environment, versions, and for the caller: `can_approve` and, if not, the reason in words (own run; no permission in that label) |
| `POST /api/v1/runs/{id}/approve` | `approve` | Body: `manifest_id`, optional `comment`. 409 `conflict` with message "The plan changed. Reload and review it again." or "This run was already decided." |
| `POST /api/v1/runs/{id}/reject` | `approve` | Body: optional `comment` |

- `GET /api/v1/runs/{id}` gains the open or last approval (who, when, decision, comment, expiry).

- [ ] **Step 1: Write the failing tests**: `TestWaitingApprovalCreatesRequestWithExpiryFromJob`; `TestApproveMovesToQueuedAndRecordsWho`; `TestApproveOwnRunRefusedButAdminAllowed`; `TestApproveWithStaleManifestIsRefused` (Review Focus 1); `TestApproveAndRejectRaceHasOneWinner` (Review Focus 2); `TestRejectMovesToRejected`; `TestExpiryMovesToExpired`; `TestApprovalsListShowsCanApproveAndReason` for three different people; endpoint permission and conflict codes; `TestDecisionsAreAudited`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration test-e2e`. Expected: pass (update earlier end-to-end tests for the new signatures).
- [ ] **Step 5: Commit** `feat: approve the exact plan a second person was shown`.

---

### Task 3: Freezes

**Files:**
- Create: `internal/engine/freeze.go`, `internal/api/freezes.go`
- Modify: `internal/engine/advance.go` (fill `View.Frozen`)
- Test: `internal/engine/freeze_integration_test.go`, `internal/api/freezes_integration_test.go`

Behaviour:
- A freeze applies to a run when its environment or its label matches an active freeze.
- A `queued` run under a freeze does not try to take locks. Its wait reason is `frozen: <reason>, until <time>`. It proceeds on a later advance once no freeze applies. (Review Focus 3)
- Planning and approving are not blocked by a freeze. Runs already `running` continue.
- The engine loop's timer picks up expired freezes without any other trigger.

Routes:

| Method and path | Needs | Does |
|---|---|---|
| `GET /api/v1/freezes` | `view` | Active freezes, and recent ended ones |
| `POST /api/v1/admin/freezes` | `manage_access` | Body: a label or an environment, a reason (required), and an expiry of at most 30 days |
| `DELETE /api/v1/admin/freezes/{id}` | `manage_access` | Lifts it |

- [ ] **Step 1: Write the failing tests**: `TestFrozenLabelBlocksQueuedRunWithReason`; `TestFrozenEnvironmentOnlyBlocksThatEnvironment`; `TestRunningRunContinuesUnderFreeze`; `TestRunStartsWhenFreezeLifted` and `TestRunStartsWhenFreezeExpires`; `TestFreezeCreatedWhileRunWaitsForLocksStillBlocksIt` (Review Focus 3); `TestPlanningAndApprovingAreNotBlocked`; endpoint validation (no reason; both scopes; expiry beyond 30 days), permission and audit.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: freeze deployments to an environment or a label`.

---

### Task 4: Messages and the outbox

**Files:**
- Create: `internal/notify/message.go`, `internal/notify/outbox.go`
- Modify: `internal/engine/advance.go` and `approval.go` (enqueue at each transition below)
- Test: `internal/notify/message_test.go`, `internal/notify/outbox_integration_test.go`, `internal/engine/notify_integration_test.go`

**Interfaces:**
- Produces:

```go
type Event struct {
    Kind        string // approval_requested | approved | rejected | approval_expired | started | finished
    Run         store.Run
    JobTitle    string
    Versions    string            // "Backend services 2.31.0 (4), Microservices 1.4.2 (20)"
    Actor       string            // who decided, for approved and rejected
    Updated, Total int            // for finished
    Failures    []Failure         // for finished
}
type Failure struct{ Service, Step, Short string }
func Text(e Event, publicURL string) string
func Enqueue(ctx context.Context, tx store.DBTX, channel string, e Event, publicURL string, now time.Time) error // no-op when channel is empty
```

Message wording (plain text, one message each, ending with the run's link `<publicURL>/runs/<id>`):
- `approval_requested`: `<requester> wants to run "<job>" in <environment> (<label>): <versions>. Review and approve:`
- `approved` / `rejected`: `Approved by <actor>.` / `Rejected by <actor>.` followed by the job and environment.
- `approval_expired`: `The approval request for "<job>" in <environment> expired.`
- `started`: `Started: "<job>" in <environment> by <requester>.`
- `finished`: `Succeeded: ...  <updated> of <total> services updated.`, or `Partly succeeded`, `Failed`, `Cancelled`, `Needs reconciliation` in the same shape, then `Failed: <service> (<short>), ...` listing at most 10 and `and <n> more`.
- Text never contains config contents, tokens or AWS error bodies beyond the short reason.

Enqueue points, each inside the transaction that makes the transition: entering `waiting_approval`; approve; reject; expire; entering `running`; reaching `succeeded`, `partially_succeeded`, `failed`, `cancelled`, or first entering `needs_reconciliation`. A job without `notify.slack` enqueues nothing.

- [ ] **Step 1: Write the failing tests**: `TestText` table, one row per kind and finished state, asserting the exact string; `TestFinishedListsAtMostTenFailures`; `TestEnqueueNoChannelIsNoOp`; `TestEachTransitionEnqueuesItsNotificationOnce` (drive a run through approval to a failed finish and assert the kinds in order, and that advancing again adds none); `TestRolledBackTransitionEnqueuesNothing`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: queue a notification with each run transition`.

---

### Task 5: Slack delivery

**Files:**
- Create: `internal/notify/slack.go`, `internal/api/slack.go`; extend `internal/notify/outbox.go` with the sender
- Modify: `internal/config/config.go` (`YUMDRAKE_SLACK_API_URL` default `https://slack.com/api`), `internal/app` (start the sender)
- Test: `internal/notify/slack_test.go`, `internal/notify/sender_integration_test.go`, `internal/api/slack_integration_test.go`

**Interfaces:**
- Produces:

```go
type Slack struct{ /* base URL, http client */ }
func (s *Slack) Post(ctx context.Context, token, channel, text string) error
type SlackError struct{ Code string; RetryAfter time.Duration; Permanent bool } // Permanent for channel_not_found, not_in_channel, invalid_auth, account_inactive, token_revoked
type Sender struct{ /* pool, slack, token source, clock, logger */ }
func (s *Sender) Run(ctx context.Context, every time.Duration)
func (s *Sender) DeliverDue(ctx context.Context) (sent, retried, failed int, err error)
```

Behaviour:
- `Post` calls `chat.postMessage`. HTTP 200 with `"ok": false` is an error carrying Slack's code. 429 carries `Retry-After`. The token never appears in an error.
- `DeliverDue` claims due notifications and posts them. Success marks sent. A permanent error marks failed at once. Any other error schedules a retry with backoff (1 minute doubling to 15 minutes) and marks failed after 12 attempts. With no Slack connection configured, notifications stay pending and are retried at the slowest interval.
- Order is kept per run.
- Routes (`manage_connections`, audited): `GET /api/v1/admin/slack` (whether a token is set, when, by whom), `PUT /api/v1/admin/slack` (body: token), `POST /api/v1/admin/slack/test` (body: channel; posts "Yumdrake can post here." and reports Slack's code on failure).

- [ ] **Step 1: Write the failing tests** against a fake Slack `httptest` server: `TestPostSendsBearerAndBody`; `TestOkFalseIsErrorWithCode`; `TestRateLimitCarriesRetryAfter`; `TestPermanentCodesAreMarkedPermanent`; `TestErrorNeverContainsToken`; `TestDeliverSendsInOrderPerRun`; `TestTransientErrorRetriesWithBackoffThenFails`; `TestPermanentErrorFailsAtOnceWithCode` (Review Focus 5); `TestSlackDownThenUpDeliversEverythingInOrder` (Review Focus 4); `TestNoConnectionKeepsPending`; `TestSlackSettingsNeverReturnToken`; `TestSlackTestEndpointReportsCode`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: deliver notifications to Slack with retries`.

---

### Task 6: End-to-end check

**Files:**
- Create: `test/e2e/governance_test.go`, `test/e2e/fakeslack/server.go`

Uses Plan 6's Floci setup (real steps), a job that needs approval in `prod`, and three people: a requester with `run` everywhere, an approver with `approve` in `prod`, and an admin.

- [ ] **Step 1: Write the failing tests**:
  - `TestProdRunWaitsIsApprovedAndDeploys`: the requester plans and starts a `prod` run; it waits; the requester's own approve is 403; the approver approves with the manifest id from the plan; it deploys; the fake Slack received, in order: request, approved, started, succeeded with counts.
  - `TestApproveStalePlanRefused`. (Review Focus 1)
  - `TestRejectedRunDoesNothing`: no step leg ran; Slack got request and rejected.
  - `TestDevRunNeedsNoApproval`.
  - `TestFreezeBlocksStartThenReleases`: freeze `prod`; an approved run stays `queued` with the freeze reason; lifting it lets the run deploy. (Review Focus 3)
  - `TestSlackOutageDoesNotAffectRuns`: fake Slack answers 500 during two complete runs; both finish normally; after it recovers every message arrives in order per run. (Review Focus 4)
- [ ] **Step 2: Run** `make test-e2e`; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration test-floci test-e2e smoke floci-clean`. Expected: all pass.
- [ ] **Step 5: Commit** `test: prove approvals, freezes and Slack end to end`.
- [ ] **Step 6: Push, open a pull request against the previous plan's branch, confirm CI passes.** Do not merge it.

---

## Parallel execution

Task 1 first. Then Tasks 2, 3 and the pure half of Task 4 (`message.go`) together. Then the rest of Task 4 and Task 5's Slack client together. Then Task 5's sender and endpoints, then Task 6.

## Definition of done

- `make lint test test-integration test-floci test-e2e smoke` passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- No new third-party module.
- A reviewer confirms the five Review Focus conditions from the named tests.
