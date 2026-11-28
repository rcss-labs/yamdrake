# Yumdrake Plan 8b: Web Application, Runs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A person can do the whole job of Yumdrake in the browser: pick an environment and versions, read the plan and diff, start, watch every step and service live with its log, cancel, approve, reconcile, re-run what failed and restore.

**Architecture:** New screens built from Plan 8a's component kit and API client. One hook owns a run's live data: it follows the event stream while the run is active and falls back to paged reads when it is finished. Every action a person can take on a run is one button with one request, shown only when the run's state and the person's permissions allow it.

**Tech Stack:** As Plan 8a. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 7, 8.3, 10, 12). The mockup `docs/mockup/yumdrake-mockup.dc.html` is the reference for the job page's History and Run tabs, the run page's facts row, "What failed" panel, steps list and console, the Runs list and Approvals. The plan-and-diff panel, the action bar, reconciliation and freezes are not in the mockup: build them from the kit as specified here, in the same visual language. API contract: route tables in Plans 4 (Task 9), 5 (Tasks 4 and 9), 6 (Task 4) and 7 (Tasks 2, 3, 5); prefer the real Go handlers where they differ.

**Builds on:** Plan 8a (merged, including its Task 9) and backend Plans 4 to 7.

No full code, by the owner's rule.

## Global Constraints

- All of Plan 8a's Global Constraints apply, including its dependency lists and visual rules. No new dependency.
- A destructive or far-reaching action (start in `prod`, cancel, reject, mark a leg succeeded or failed, release locks, restore) asks for confirmation in a dialog that says in one sentence what will happen and what will not. Cancel's dialog says: "Steps that have not started will not run. Services already rolling will finish. Nothing is rolled back."
- Buttons for actions the person may not take are not shown. Buttons for actions the run's state does not allow are not shown. A 409 from the server (state changed meanwhile) reloads the run and shows what changed.
- The plan and diff never show a config value. If the API ever returned one, the UI would have no place to render it: env diffs render key names only.
- Log text is rendered as text, never as HTML.
- Times are shown relative ("3 minutes ago") with the exact time as a tooltip and in the facts row. Durations as `2m 14s`.
- The console keeps at most 20,000 lines in memory; past that it says so and offers the download link for the full log.

## Review Focus

1. The connection drops while watching a run: the page shows "Reconnecting", then catches up without a missing or repeated log line, and the status is current. (Task 6)
2. Someone else changes the run while the page is open (approves it, cancels it): the page updates by itself and its buttons change with it; pressing a now-invalid button gives a clear message, not an error page. (Tasks 4, 8)
3. A plan with a failed check or a fallback branch: it is impossible to miss, and Start is absent when a check failed. (Task 5)
4. A run with 35 services and several thousand log lines: the page stays responsive, and filtering to one failed service takes one click. (Task 6)
5. The person may view a run but not its logs: the steps and states show, the console says "You do not have access to logs in this environment", and no log request is made. (Task 6)

## File Structure

| Path | Responsibility |
|---|---|
| `web/src/api/types.ts`, `hooks.ts`, `web/src/test/handlers.ts`, `fixtures.ts` | Types, hooks and mock routes for runs, plans, versions, approvals, freezes, Slack |
| `web/src/features/runs/useRunLive.ts` | A run's live state and log |
| `web/src/test/eventSource.ts` | A controllable `EventSource` for tests |
| `web/src/features/jobs/RunTab.tsx`, `HistoryTab.tsx` | The job page's two remaining tabs |
| `web/src/features/runs/RunsTable.tsx`, `RunsPage.tsx` | Run lists |
| `web/src/features/runs/RunPage.tsx`, `RunActions.tsx`, `RunFacts.tsx`, `WhatFailed.tsx` | The run page frame |
| `web/src/features/runs/PlanPanel.tsx` | Plan and diff |
| `web/src/features/runs/StepsTree.tsx`, `Console.tsx` | Steps, services and the log |
| `web/src/features/runs/Reconcile.tsx` | Unknown outcomes |
| `web/src/features/approvals/ApprovalsPage.tsx` | Approvals |
| `web/src/features/freezes/FreezeBanner.tsx`, `web/src/features/admin/FreezesSection.tsx`, `SlackSection.tsx` | Freezes and Slack |
| `web/src/ui/Dialog.tsx` | Confirmation dialog (native `dialog` element) |
| `web/e2e/runs.spec.ts` | Browser test against the real backend and Floci |

---

### Task 1: Data layer for runs

**Files:**
- Modify: `web/src/api/types.ts`, `hooks.ts`, `web/src/test/handlers.ts`, `fixtures.ts`
- Create: `web/src/features/runs/useRunLive.ts`, `web/src/test/eventSource.ts`, `web/src/ui/Dialog.tsx`
- Test: `web/src/features/runs/useRunLive.test.tsx`, `web/src/ui/Dialog.test.tsx`

**Interfaces:**
- Produces:

```ts
interface LogLine { runId: number; legId: number; seq: number; at: string; level: "info" | "ok" | "warn" | "error"; text: string }
interface RunLive {
  run: RunDetail | undefined          // steps, legs, summary, wait reason, approval, locks
  lines: LogLine[]                    // ordered by time, then leg, then seq
  gaps: { legId: number; fromSeq: number; toSeq: number }[]
  connection: "live" | "reconnecting" | "finished" | "no-access"
  truncated: boolean                  // more than 20,000 lines
  error: ApiError | undefined
}
function useRunLive(runId: number, opts: { logs: boolean }): RunLive
function ConfirmDialog(props: { open: boolean; title: string; body: string; confirmLabel: string; danger?: boolean; onConfirm(): void; onCancel(): void }): JSX.Element
```

Behaviour of `useRunLive`:
- Loads `GET /runs/{id}`. If the run is in a final state: reads the log in pages of 1000 until done, then `connection` is `finished`.
- Otherwise opens `EventSource` on `/runs/{id}/events`. `state` events replace `run`. `log` events append lines, dropping any whose `(legId, seq)` it already has. `end` closes the stream and sets `finished`. While the browser is retrying the connection, `connection` is `reconnecting`. (Review Focus 1)
- With `logs: false` it opens the stream but ignores log events and makes no log request, and `connection` reports `no-access` for the console. (Review Focus 5)
- Mock routes cover every run endpoint of Plans 4 to 7 over fixtures for: a planned run with a diff, a waiting-approval run, a running run, a partly succeeded run with one failed service and skipped legs, a run needing reconciliation, an invalidated run, and a succeeded restore.

- [ ] **Step 1: Write the failing tests**: finished run reads all pages in order; active run applies `state` then `log` events; a repeated `log` batch adds nothing; an `error` then reopen shows `reconnecting` then `live` with no duplicate (Review Focus 1); `end` closes; `logs: false` makes no log request; more than 20,000 lines sets `truncated` and keeps the newest; `ConfirmDialog` traps focus, closes on Escape, and calls `onConfirm` once.
- [ ] **Step 2: Run** `make web-test`; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the live run data layer`.

---

### Task 2: Run tab

**Files:**
- Create: `web/src/features/jobs/RunTab.tsx`; modify `JobPage.tsx` (enable the tab and the "Run this job" button)
- Test: `web/src/features/jobs/RunTab.test.tsx`

As in the mockup's Run tab, with planning instead of an immediate start:
- Environment select listing the environments whose label the job permits, each shown with its label. The choice is kept in the URL.
- For that environment, `GET /jobs/{id}/versions`. One card per group: a checkbox for the whole group with "N of M selected", a version select of the group's common tags (newest first, the first marked "newest"), and for groups with more than one service an expandable list with a checkbox and a version override per service. A service that does not exist in the environment, or whose versions could not be read, is disabled with the reason.
- A summary card: how many services, which versions, and a note if the job needs approval in this label. A `FreezeBanner` if the environment is frozen.
- One primary button, "Plan this run". It posts the selection with `start: false` and goes to the new run's page. A 422 shows the problems next to the form.
- Without `run` permission for this label the form is read-only and says "Your role may not run jobs in <label>".
- A not-runnable job shows why instead of the form.

- [ ] **Step 1: Write the failing tests**: environments limited to the job's labels; group and service selection counts; version defaults to the newest common tag; an override posts per-service; an unavailable service is disabled with its reason; nothing selected disables the button; the posted body matches the selection exactly and has `start: false`; 422 problems are shown; read-only without permission; not-runnable job; frozen environment shows the banner; loading and error states for versions.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): choose services and versions and plan a run`.

---

### Task 3: History tab and Runs page

**Files:**
- Create: `web/src/features/runs/RunsTable.tsx`, `RunsPage.tsx`, `web/src/features/jobs/HistoryTab.tsx`; modify `JobsPage.tsx` (fill the "Last run" column) and `JobPage.tsx` (facts row: last run, success rate of finished runs, last success, run count)
- Test: `web/src/features/runs/RunsTable.test.tsx`, `RunsPage.test.tsx`, `HistoryTab.test.tsx`

`RunsTable` columns, as in the mockup: Run (number, linking to the run), Status (`StatusPill`), Job (omitted on a job's own history), Environment (mono, with a label `Tag`), Services (`ServiceStrip` and "19/20", versions beneath), Started (who, relative time, via), Duration, What failed (short reasons; empty for a clean run). Restore runs are marked "Restore of #N".

Filters above the table: a status `Segmented` (All, Failed, Succeeded, Running, Waiting), an environment select, and on the Runs page a search by job, service or person. Filters live in the URL. "Load older" pages with `before`.

- [ ] **Step 1: Write the failing tests**: rows render strip cells with the right statuses and titles; "What failed" lists short reasons; filters change the request query and the URL; empty and no-match states; "Load older"; the job facts row computes success rate from finished runs only; the Jobs list shows each job's last run.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add run history and the Runs list`.

---

### Task 4: Run page frame and actions

**Files:**
- Create: `web/src/features/runs/RunPage.tsx`, `RunActions.tsx`, `RunFacts.tsx`, `WhatFailed.tsx`
- Test: `web/src/features/runs/RunPage.test.tsx`, `RunActions.test.tsx`

Layout, top to bottom: crumbs (Jobs, the job, Run #N); title with the `StatusPill`; one sentence saying where the run stands; the action bar; the facts row (environment, started by and via, started, duration, versions, services updated with the large strip); then, depending on state, the banner or panel below; then the plan (Task 5), the steps and console (Task 6).

The sentence and what is shown per state:

| State | Sentence | Shown |
|---|---|---|
| `resolving` | "Planning: reading current versions and config" | Loading plan |
| `planned` | "Review the plan, then start" | Plan open. Actions: Start, Discard |
| `waiting_approval` | "Waiting for approval, expires <time>" | Approve and Reject for people who may, with the reason for those who may not. Action: Cancel |
| `queued` | The wait reason in words: "Waiting for run #N to finish" (linked) or "Frozen: <reason>, until <time>" | Action: Cancel |
| `running` | "<n> of <m> services updated so far" | Action: Cancel |
| `cancelling` | "Cancelling: waiting for services already rolling to finish" | |
| `needs_reconciliation` | "<n> outcomes could not be confirmed. Its services stay locked until they are resolved" | Reconcile panel (Task 7) |
| `succeeded` | "All <m> services updated" | Action: Restore |
| `partially_succeeded`, `failed` | "<n> of <m> services updated" | `WhatFailed`. Actions: Re-run failed, Restore (if anything changed) |
| `cancelled` | "Cancelled by <who>. <n> services had already been updated" | Actions: Re-run failed, Restore (if anything changed) |
| `invalidated` | "The targets changed after this plan was made" with the detail | Action: Plan again |
| `rejected`, `expired` | Who and when | Action: Plan again |

Actions and their permissions: Start and Discard and Plan again and Re-run failed need `run`; Cancel needs `cancel`; Approve and Reject need `approve` and the server's `can_approve`; Restore needs `restore`. Start, Approve send the manifest id the page is showing. Plan again creates a new run from the same selection and goes to it. Restore opens a dialog listing the services that will go back, then creates the restore run and goes to it. Each confirmed action follows the Global Constraints wording.

`WhatFailed`: the mockup's panel. A table of failed item, step, the full reason, and "Show its log", which sets the console's scope (Task 6); and a line per skipped step.

- [ ] **Step 1: Write the failing tests**: one test per state row asserting the sentence and exactly which buttons exist for an admin; the same for a person with only `view` (no buttons); Start in `prod` asks for confirmation and posts the manifest id; Cancel's dialog wording; a 409 on Approve reloads and shows "This run was already decided" (Review Focus 2); a `state` event that moves the run from `waiting_approval` to `running` swaps the buttons without reload (Review Focus 2); Plan again posts the original selection; Restore lists services and navigates to the new run; `WhatFailed` rows and "Show its log".
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the run page with state-aware actions`.

---

### Task 5: Plan and diff

**Files:**
- Create: `web/src/features/runs/PlanPanel.tsx`
- Test: `web/src/features/runs/PlanPanel.test.tsx`

From `GET /runs/{id}/plan`. A card titled "Plan", open while the run is `planned` or `waiting_approval`, collapsed to one line afterwards ("Plan: 24 services, 3 config files change").

- At the top, only if present, in this order and impossible to miss (Review Focus 3): failed checks as an error `Note` listing each check, its service and its detail, with the sentence "This run cannot start"; fallback use as a warning `Note`: "Env files for <services> come from branch dev because branch v<version> does not exist"; unverified checks as an info line.
- One table, a row per service: service (mono), from version to version (the old one muted; "unchanged" when equal), the previous task definition's revision, and config: for each item its name and `new`, `changed` or `unchanged`. A changed env file expands to three short lists of key names: added, removed, changed. A TOML only says changed or not.
- A restore plan labels the columns "now" and "restored to", and shows `NotRestored` reasons as warnings on their items.
- No value is ever rendered; the component has no prop that could carry one.

- [ ] **Step 1: Write the failing tests**: version change and unchanged rendering; env key lists; TOML shows no keys; failed check shows the error note and the page hides Start (Review Focus 3); fallback warning names services and branch; `not_verified` line; collapsed summary counts; restore wording and `NotRestored` warnings; a fixture env value never appears in the document.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): show the plan and diff before a run starts`.

---

### Task 6: Steps, services and the console

**Files:**
- Create: `web/src/features/runs/StepsTree.tsx`, `Console.tsx`, `console.css`
- Test: `web/src/features/runs/StepsTree.test.tsx`, `Console.test.tsx`

As in the mockup's two-pane run view.

`StepsTree`: a list of buttons. First "Whole run" with the line count. Then each step (name, type or "19/20", status shape), and under a step with services, each service (mono name, version, status shape). Failed and unknown services sort to the top of their step. The selected item sets the console's scope and is kept in the URL (`?step=`, `?service=`). Attempts above 1 show "attempt 2".

`Console`: the dark panel. Header: the scope's name, "<shown> of <total> lines, <n> error lines", a text filter, an "Errors only" toggle, and a "Download" link to the log endpoint for the current scope. Body: each line with its number, time and text; error lines tinted; warn and ok coloured as in the mockup. A gap is rendered as its own row: "Lines <a> to <b> of this step are missing".
- Scope, filter and errors-only are applied in the browser over the lines `useRunLive` holds. Selecting a service also shows that service's lines from its config step.
- While the run is active and the person is at the bottom, new lines keep the view at the bottom; if they scrolled up it stays put and a "Jump to latest" button appears.
- Rows use `content-visibility: auto` so thousands of lines stay cheap. (Review Focus 4)
- A connection state strip: nothing when live, "Reconnecting" when retrying, "You do not have access to logs in this environment" for `no-access`. (Review Focus 5)
- When `truncated`, a notice with the download link.

- [ ] **Step 1: Write the failing tests**: tree order with failed services first; selecting a service filters to its lines including its config lines; filter text and errors-only combine; counts in the header; gap row; text containing `<script>` is shown literally; follow-tail behaviour with a mocked scroll position; "Reconnecting" strip (Review Focus 1); `no-access` shows the message and the mock server saw no log request (Review Focus 5); 5,000 lines render in under 1.5s in jsdom and one click on a failed service shows only its lines (Review Focus 4); truncated notice.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the steps tree and live console`.

---

### Task 7: Reconciliation

**Files:**
- Create: `web/src/features/runs/Reconcile.tsx`
- Test: `web/src/features/runs/Reconcile.test.tsx`

Shown on a run in `needs_reconciliation`. A warning panel: "Yumdrake could not confirm what happened to these. Check them in AWS, then say what happened." A row per unknown leg: service, step, what was intended (from the leg's last recorded operation, for example "Update service to backend-api:43"), the failure detail, "Show its log", and for people with `restore`: "Check again", "Mark succeeded", "Mark failed". The two marks ask for confirmation naming the service. Admins also get "Release locks without resolving", whose dialog says the run will be marked failed and other runs may then touch these services.

- [ ] **Step 1: Write the failing tests**: rows show the intended operation; each button calls its route; confirmations name the service; buttons absent without `restore`; "Release locks" only for admins; after the last unknown leg is resolved the panel disappears when the next `state` event arrives.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): resolve unknown outcomes from the run page`.

---

### Task 8: Approvals, freezes and Slack

**Files:**
- Create: `web/src/features/approvals/ApprovalsPage.tsx`, `web/src/features/freezes/FreezeBanner.tsx`, `web/src/features/admin/FreezesSection.tsx`, `SlackSection.tsx`; modify `Sidebar.tsx` (a count on Approvals), `AdminPage.tsx`
- Test: one test file per component

- Approvals page, as in the mockup: a card per waiting run saying who wants to run what where, the versions, when it was asked and when it expires, the reason the caller may or may not approve, and Details (the run page), Approve, Reject. Approve opens the run page's plan first if the person has not seen it: the button on this page is "Review and approve", which goes to the run page. The sidebar shows the number waiting that the caller may approve, refreshed every 30 seconds.
- `FreezeBanner`: for an environment or label, "Deployments to <scope> are frozen until <time>: <reason>".
- Admin, Freezes (`manage_access`): active freezes with scope, reason, who, until when, and "Lift"; a form to freeze a label or an environment with a required reason and an expiry (presets: 1 hour, 4 hours, 1 day, custom up to 30 days); a list of recent ended freezes.
- Admin, Slack (`manage_connections`): whether a token is set and since when, a field to set a new one, and a test that posts to a channel and shows Slack's answer in words.

- [ ] **Step 1: Write the failing tests**: approvals list with can and cannot approve reasons; empty state; the sidebar count; banner wording for label and environment; freeze form validation (reason required, expiry at most 30 days) and lift with confirmation; Slack token field cleared after save and never shown; Slack test success and `channel_not_found` shown as "Slack could not find that channel".
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test web-build`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add approvals, freezes and Slack settings`.

---

### Task 9: Browser test against the real system

**Files:**
- Create: `web/e2e/runs.spec.ts`; extend the Playwright setup from Plan 8a to seed Floci (reusing `test/awstest` through a small Go seeding command, `go run ./test/awstest/cmd/seed`, that prints the seeded ids as JSON) and to start a runner

- [ ] **Step 1: Write the failing test** covering, in one flow against orchestrator, runner, PostgreSQL, Valkey, Floci and the fake GitHub: sign in as a person with `run` in dev; open the job; choose `hetzner-01`, pick version `1.4.2` for the group with one service overridden to a version whose image crashes; plan; the plan shows the version changes and a changed env file's key names; start; watch the strip fill; the run ends partly succeeded; "What failed" names the service and reason; "Show its log" narrows the console to that service and an error line with the exit code is visible; "Re-run failed" with a good version succeeds; "Restore" on the first run shows a plan back to `1.4.1` and, once started, succeeds. Then a `prod` run: it waits for approval; a second signed-in person approves from the Approvals page; it deploys.
- [ ] **Step 2: Run** `make web-e2e`; confirm failure.
- [ ] **Step 3: Implement** the seeding command and the test.
- [ ] **Step 4: Run** `make lint build test test-integration test-floci test-e2e web-lint web-test web-e2e smoke floci-clean`. Expected: all pass.
- [ ] **Step 5: Commit** `test(web): run the whole deployment flow in a real browser`.
- [ ] **Step 6: Push, open a pull request, confirm CI passes.** Do not merge it.

---

## Parallel execution

Task 1 first. Then Tasks 2, 3, 5, 7 and 8 together, and Task 4 and 6 together with them (4 composes 5, 6 and 7: agree their props from this plan first, build against placeholders, and wire them when they merge). Task 9 last.

## Definition of done

- The command in Task 9 Step 4 passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- No new dependency.
- A reviewer walks every state of the table in Task 4 in the running app and confirms the five Review Focus conditions.
