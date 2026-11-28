# Yumdrake Plan 8a: Web Application Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The React application exists with its design system, sign-in, navigation and every screen that does not involve a run: Jobs, a job's definition, Inventory, Admin and CLI tokens. It is built and tested on its own, against a mocked API, so it can be developed while the backend plans are still in progress.

**Architecture:** A Vite single-page app under `web/`, in TypeScript. One small typed API client; server state through a query library; screens composed from a hand-built component kit that carries the mockup's tokens. Tests run against Mock Service Worker handlers that follow the route tables of Plans 2, 3 and 5. A final task, done after Plan 3 has merged, embeds the built app in the orchestrator and checks it against the real backend in a browser.

**Tech Stack:** React 19, TypeScript, Vite, React Router, TanStack Query, Vitest, Testing Library, Mock Service Worker, ESLint with typescript-eslint, Playwright (last task only). Plain CSS with custom properties; no component library and no CSS framework.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 9.2, 12, 13). The mockup source `docs/mockup/yumdrake-mockup.dc.html` is the visual reference: its `<style>` block has the tokens and component styles, its markup has each screen's layout. API contract: the route tables in `docs/superpowers/plans/2026-11-27-yumdrake-02-yaml-and-sync.md` (Task 8), `...-03-sign-in-and-permissions.md` (Tasks 5 to 7) and `...-05-manifest-and-preflight.md` (Task 2).

**Works in:** `web/` only, until Task 9. Tasks 1 to 8 must not change any Go file.

No full code, by the owner's rule.

## Global Constraints

- Node 24 and npm. Every dependency is saved with an exact version (`save-exact`) and `package-lock.json` is committed. Run `npm audit --omit=dev` after adding dependencies; a high or critical finding stops the task and is escalated.
- Runtime dependencies allowed: `react`, `react-dom`, `react-router-dom`, `@tanstack/react-query`, `@fontsource/ibm-plex-sans`, `@fontsource/ibm-plex-mono`. Development dependencies allowed: `vite`, `@vitejs/plugin-react`, `typescript`, `vitest`, `jsdom`, `@testing-library/react`, `@testing-library/user-event`, `@testing-library/jest-dom`, `msw`, `eslint`, `typescript-eslint`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, `@playwright/test`, and type packages. Anything else needs an escalation with a recorded evaluation of maintenance, security and supply-chain risk.
- TypeScript `strict`. No `any` in application code. No `dangerouslySetInnerHTML`.
- Test first: write the test, see it fail for the stated reason, then implement. Tests assert what a person sees and can do (roles, labels, text), not implementation details.
- Visual rules from the mockup, all of them:
  - Tokens are CSS custom properties in one file, copied from the mockup's `:root` block with the same names and values. Components use tokens, never raw colours.
  - IBM Plex Sans for text, IBM Plex Mono for ids, versions, paths and code, both self-hosted.
  - Colour is reserved for status. A status is always shown with a shape and a label as well as colour.
  - Sentence case everywhere. No emoji. No exclamation marks. Buttons say what they do ("Add job", "Create folder").
  - Spacing on a 4px grid. Controls are 36px tall; table rows at least 40px.
  - Every interactive element is a real `button`, `a` or form control with an accessible name, has a visible focus ring, and works from the keyboard.
  - Layout works from 360px wide: the sidebar stacks above the content, toolbars wrap, wide tables scroll inside their card.
- Permissions: the UI hides or disables what the signed-in person may not do, using the grants from `GET /api/v1/me`, and still handles a 403 from the server by showing a clear message.
- Errors from the API are shown in plain words next to what failed. A 401 anywhere sends the person to the sign-in page and brings them back afterwards.
- No secret entered in a form is kept in state after submit, logged, or put in a URL.
- Make targets added in Task 1 are the only way CI and people run things: `web-install`, `web-lint`, `web-test`, `web-build`.

## Review Focus

1. The session expires while a page is open: the next action leads to sign-in and returns to the same page, with no lost form silently submitted twice. (Task 3)
2. The API is slow or down: each screen shows a loading state, then a readable error with a retry, never a blank page or an endless spinner. (Tasks 3, 5 to 8)
3. A person without `manage_jobs` or `manage_access`: they see no controls for it, a direct URL to an admin screen shows "You do not have access", and nothing is requested that the server would refuse. (Tasks 4, 8)
4. A list is empty, or a search matches nothing: the screen says so and says what to do next. (Tasks 5 to 8)
5. A YAML file has problems: the job or inventory screen shows each problem with its file and line, and shows that the last good version is still in use. (Tasks 6, 7)

## File Structure

| Path | Responsibility |
|---|---|
| `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/eslint.config.js` | Tooling |
| `web/src/main.tsx`, `web/src/App.tsx`, `web/src/routes.tsx` | Entry, providers, route table |
| `web/src/styles/tokens.css`, `web/src/styles/base.css` | Tokens and element defaults |
| `web/src/ui/*.tsx` with `web/src/ui/*.css` | The component kit, one file per component |
| `web/src/api/client.ts`, `web/src/api/types.ts`, `web/src/api/hooks.ts` | Typed API access |
| `web/src/auth/*` | Session, permissions, sign-in |
| `web/src/shell/*` | Sidebar, page frame, error and not-found pages |
| `web/src/features/jobs/*`, `inventory/*`, `admin/*`, `tokens/*` | Screens |
| `web/src/test/server.ts`, `web/src/test/handlers.ts`, `web/src/test/fixtures.ts`, `web/src/test/render.tsx` | Mock API and test helpers |
| `internal/webui/webui.go` (Task 9) | Serve the built app from the orchestrator |

---

### Task 1: Scaffold and tooling

**Files:**
- Create: everything under "Tooling" above, `web/index.html`, `web/src/main.tsx`, `web/src/App.tsx`, `web/src/styles/tokens.css`, `web/src/styles/base.css`, `web/src/test/render.tsx`, `web/.gitignore`
- Modify: `Makefile` (the four `web-*` targets), `.github/workflows/ci.yml` (a `web` job running lint, test and build with Node 24 and the npm cache), `.dockerignore`, `README.md`
- Test: `web/src/App.test.tsx`

- Vite dev server proxies `/api` and `/saml` to `http://localhost:8080`.
- `tokens.css` holds exactly the custom properties of the mockup's `:root,body{...}` rule. `base.css` holds the element defaults (body font and size, headings, focus ring, `.mono`, `.num`).
- `render.tsx` exports `renderApp(ui, { route, me })`, wrapping in the router, a fresh query client with retries off, and the session provider from Task 3 once it exists.

- [ ] **Step 1: Write the failing test** `App renders the product name`: rendering `<App />` shows the text "Yumdrake".
- [ ] **Step 2: Run** `make web-test`; confirm it fails because nothing exists.
- [ ] **Step 3: Create** the scaffold with exact-version dependencies. Add the Make targets and the CI job.
- [ ] **Step 4: Run** `make web-install web-lint web-test web-build` and `npm audit --omit=dev` in `web/`. Expected: all pass, no high or critical finding, `web/dist/index.html` exists.
- [ ] **Step 5: Commit** `feat(web): scaffold the React application and its tooling`.

---

### Task 2: Component kit

**Files:**
- Create: `web/src/ui/` components listed below, each with its CSS and a test file
- Test: `web/src/ui/*.test.tsx`

Components, with the props that matter (each takes `className` and passes other DOM props through):

| Component | Props and behaviour |
|---|---|
| `Button` | `variant: "default" \| "primary" \| "danger"`, `size: "md" \| "sm"`, `loading`. While loading it is disabled and keeps its width |
| `Field` | `label`, `hint`, `error`; wraps one control and ties label, hint and error to it for assistive technology |
| `TextInput`, `Select`, `Checkbox` | Native controls styled to tokens |
| `SearchBox` | `value`, `onChange`, `label` (used as accessible name and placeholder) |
| `Segmented` | `options: {value, label}[]`, `value`, `onChange`, `label`. Buttons with `aria-pressed` |
| `Tabs` | `tabs: {id, label}[]`, `active`, `onChange`. Proper tab roles, arrow-key movement |
| `Card` | `flush` removes padding for tables |
| `DataTable` | `columns`, `rows`, `empty` (node shown when no rows), `minWidth`. Scrolls sideways inside itself |
| `StatusPill` | `status: RunStatus \| LegStatus \| "never"`, optional `label`. Draws the shape for that status and the label; the label defaults to the plain-words name |
| `Tag` | Small neutral label, for environment labels and step types |
| `ServiceStrip` | `cells: {status, title}[]`, `size: "sm" \| "lg"`, `label`. One cell per service; failed cells taller; a group break is a cell with `gapBefore` |
| `PageHeader` | `title`, `lead`, `actions`, `crumbs` |
| `Facts` | `items: {label, value}[]` in one bordered row that wraps |
| `Note` | `tone: "info" \| "warning" \| "error"` |
| `EmptyState` | `title`, `children`, optional action |
| `ErrorState` | `error: ApiError \| Error`, `onRetry` |
| `Loading` | A quiet skeleton line; announces "Loading" to assistive technology |
| `CodeBlock` | Monospace block on the dark console surface, scrolls both ways, `label` |
| `ProblemList` | `problems: {file, line, message}[]`, each shown as `file:line` in mono and the message |
| `Icon` | `name` from the fixed set the mockup uses (overview, jobs, runs, approvals, versions, inventory, cli, admin, search, folder), 16px, stroke 1.5, `aria-hidden` |

Status names and labels (`RunStatus`): resolving "Planning", planned "Planned", waiting_approval "Waiting for approval", queued "Queued", running "Running", cancelling "Cancelling", needs_reconciliation "Needs reconciliation", succeeded "Succeeded", partially_succeeded "Partly succeeded", failed "Failed", cancelled "Cancelled", rejected "Rejected", expired "Expired", invalidated "Plan out of date". (`LegStatus`): pending "Not started", queued "Queued", claimed "Starting", running "Running", succeeded "Succeeded", failed "Failed", skipped "Skipped", cancelled "Cancelled", unknown "Unknown".

Shapes: succeeded a filled circle; failed and unknown a diamond; running and claimed a ring; waiting, queued, planned and resolving two bars; skipped a dash; pending and never an empty ring; cancelled, rejected, expired and invalidated a square. Every status maps to exactly one of the mockup's tone pairs (`ok`, `bad`, `run`, `wait`, or neutral).

- [ ] **Step 1: Write the failing tests**, one file per component, covering: `Button` loading disables and keeps the label readable; `Field` links label, hint and error (query by label text finds the input; the error is announced); `Segmented` and `Tabs` report selection and move with the keyboard; `DataTable` shows the `empty` node with no rows; `StatusPill` renders a label for every status in both lists and a `data-shape` that differs between succeeded, failed, running and waiting (table-driven over all statuses, failing if one is missing); `ServiceStrip` exposes its `label` as an image name and one titled cell per item; `ErrorState` shows the server's message and calls `onRetry`; `ProblemList` shows `services/orders.yaml:14`.
- [ ] **Step 2: Run** `make web-test`; confirm failure for missing modules.
- [ ] **Step 3: Implement** the components, taking sizes, radii and colours from the mockup's style block.
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass, with no accessibility lint warnings.
- [ ] **Step 5: Commit** `feat(web): add the component kit`.

---

### Task 3: API client, session and sign-in

**Files:**
- Create: `web/src/api/client.ts`, `web/src/api/types.ts`, `web/src/api/hooks.ts`, `web/src/auth/session.tsx`, `web/src/auth/permissions.ts`, `web/src/auth/SignIn.tsx`, `web/src/test/server.ts`, `web/src/test/handlers.ts`, `web/src/test/fixtures.ts`
- Test: `web/src/api/client.test.ts`, `web/src/auth/permissions.test.ts`, `web/src/auth/SignIn.test.tsx`, `web/src/auth/session.test.tsx`

**Interfaces:**
- Produces:

```ts
class ApiError extends Error { status: number; code: string }            // code from {"error":{"code","message"}}; "network" when the request never completed
function api<T>(method: string, path: string, body?: unknown): Promise<T> // JSON, same-origin credentials, 15s timeout
type Action = "view" | "view_logs" | "run" | "cancel" | "approve" | "restore" | "manage_jobs" | "manage_connections" | "manage_access" | "view_audit"
interface Grant { action: Action; label: string; environment: string; folder_id: number }
interface Me { id: number; email: string; name: string; admin: boolean; grants: Grant[] }
interface Target { label?: string; environment?: string; folderId?: number }
function can(me: Me, action: Action, target?: Target): boolean            // the same rule as the server (Plan 3, Task 2)
function canSomewhere(me: Me, action: Action): boolean                    // true if any grant has the action; for showing navigation
function useMe(): Me                                                      // throws outside a signed-in tree
```

`types.ts` declares the response and request shapes for every route in the contract tables named under **Spec**. `handlers.ts` implements every one of those routes over in-memory fixtures (five environments, two services, three folders, four jobs of which one has problems, two groups, three users, grants, an audit list, connections), with helpers `signInAs(me)`, `failNext(method, path, status, code)` and `delayNext(method, path, ms)`.

Behaviour:
- The session provider loads `/api/v1/me` once. 401 means signed out: render the sign-in page at `/sign-in?next=<current path>`. Any later 401 from any request clears the session and does the same.
- Sign-in page: email and password form posting to `/api/v1/auth/login`; a "Sign in with company SSO" link to `/api/v1/auth/saml/start` shown when `GET /api/v1/auth/methods` says SAML is enabled (treat a 404 from that route as disabled). Wrong credentials: "Email or password is not correct". 429: "Too many attempts. Try again in N minutes" from `Retry-After`. 503: the server's message. After success go to `next` if it is a path on this site, otherwise `/`.
- The password field is cleared after every attempt.
- Sign out posts to `/api/v1/auth/logout` and shows the sign-in page.

- [ ] **Step 1: Write the failing tests**: `api` parses the error body into `ApiError`, maps a network failure to code `network`, and times out; `can` table matching Plan 3's `TestCan` cases exactly, plus `canSomewhere`; sign-in success navigates to `next`; an external `next` (`//evil.example`, `https://evil.example`) is ignored; wrong password, 429 with `Retry-After: 600` and 503 show their messages; the password input is empty after a failed attempt; a 401 on a later request shows sign-in with `next` set to the current path (Review Focus 1); SSO link hidden when methods returns 404.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the API client, session handling and sign-in`.

---

### Task 4: Shell and navigation

**Files:**
- Create: `web/src/shell/Shell.tsx`, `web/src/shell/Sidebar.tsx`, `web/src/shell/NotFound.tsx`, `web/src/shell/NoAccess.tsx`, `web/src/routes.tsx`
- Test: `web/src/shell/Shell.test.tsx`

Routes: `/` (Overview, a placeholder card saying it arrives with run data), `/jobs`, `/jobs/:id` with tab in `?tab=`, `/runs` and `/approvals` and `/versions` (placeholders), `/inventory` with `?tab=`, `/cli`, `/admin/:section?`, `/cli/confirm`, `/sign-in`, and a not-found page.

Sidebar: product mark and name; Overview, Jobs, Runs, Approvals; a gap; Versions, Inventory; a gap; CLI, and Admin only when `me.admin` or `canSomewhere` for a `manage_*` action or `view_audit`. The current section is marked with `aria-current="page"`. Below: the person's name and email and "Sign out".

An admin route opened without the needed permission renders `NoAccess` and requests nothing from admin endpoints. (Review Focus 3)

- [ ] **Step 1: Write the failing tests**: the sidebar lists the eight items for an admin and seven for a person with no grants; `aria-current` follows the route; `/admin/users` as a non-admin shows "You do not have access" and the mock server saw no `/api/v1/admin/*` request; an unknown path shows "Page not found" with a link to Jobs; the layout has one `main` landmark and a labelled navigation.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the application shell and navigation`.

---

### Task 5: Jobs

**Files:**
- Create: `web/src/features/jobs/JobsPage.tsx`, `AddJobForm.tsx`, `NewFolderForm.tsx`, `jobs.css`
- Test: `web/src/features/jobs/JobsPage.test.tsx`

Follows the mockup's Jobs screen: page header with the count line and, for people with `manage_jobs`, "New folder" and "Add job"; a search box and a folder filter; one table grouped by folder with a group row per folder. Columns: Job (title linking to the job, id in mono), Deploys (number of services, or the one service's name), Approval (labels, or "None"), State (runnable, or "Has problems" with the count), Last run ("No runs yet" until Plan 8b), and for `manage_jobs` a folder select that moves the job.

- Adding a job posts repo, branch, path and folder; on success go to the new job's page; a 409 says "This file is already registered as a job"; field errors appear on their fields.
- Creating a folder shows it at once, selected in the filter.
- Moving a job updates the row in place and restores it with a message if the server refuses.

- [ ] **Step 1: Write the failing tests**: jobs are grouped under their folders with counts; search narrows by title and id; the folder filter shows one group; an empty folder says "No jobs in this folder yet"; a search with no match says "No job matches your search" (Review Focus 4); a person without `manage_jobs` sees no buttons and no folder selects (Review Focus 3); add job happy path, 409 and field error; new folder; move job success and refusal; loading then error with retry when the list request fails (Review Focus 2).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the Jobs screen`.

---

### Task 6: Job page, definition

**Files:**
- Create: `web/src/features/jobs/JobPage.tsx`, `DefinitionTab.tsx`
- Test: `web/src/features/jobs/JobPage.test.tsx`

Header as in the mockup: crumbs (Jobs, folder), title, a meta row (job id in mono, "Deploys N services", "Needs approval in prod" or "No approval needed"), and a disabled "Run this job" button with the hint "Running jobs arrives with the run screens". Tabs: History and Run (each a placeholder card for now) and Definition.

Definition tab:
- Where it is read from (repository, branch, path), the commit, how long ago it was synced and checked, and for `manage_jobs` a "Re-read from GitHub" button.
- If the newest commit has problems: a warning saying the last good version (its commit) is still in use, and the `ProblemList`. If the job is not runnable, an error note with the reason. (Review Focus 5)
- Groups: each with its label and its services in mono.
- Steps: each with its name, its type as a `Tag`, its group, what it needs in words ("After config-backends, per service" or "After config-backends, the whole step"), and `max_parallel` and `timeout` when set.
- The raw YAML in a `CodeBlock` when the API response includes `source`; otherwise nothing.

- [ ] **Step 1: Write the failing tests**: header facts for a two-group job; an unknown id shows not found; the definition lists groups, services and steps with the dependency wording for both scopes; a job with problems shows the warning, the last good commit and each problem as `file:line`; a not-runnable job shows the reason; "Re-read" calls the sync route and refreshes, and is absent without `manage_jobs`; the tab is kept in the URL.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the job page and its definition tab`.

---

### Task 7: Inventory

**Files:**
- Create: `web/src/features/inventory/InventoryPage.tsx`, `EnvironmentsTab.tsx`, `ServicesTab.tsx`
- Test: `web/src/features/inventory/InventoryPage.test.tsx`

- Header: where the inventory is read from, the active commit, when it was activated and last checked, and for `manage_connections` a "Sync from git" button.
- If a newer commit was rejected: a warning that the inventory shown is the last good one, with the rejected commit and its problems. (Review Focus 5)
- If no inventory source is set: an empty state that says so and, for `manage_connections`, links to Admin, Git sources.
- Environments tab: a table of name (mono), label (`Tag`), AWS account (mono), and how many services exist there.
- Services tab: a searchable list on the left (name, and "all environments" or "N of M environments"); on the right the chosen service: its version rule, and one block per environment with the ECS service, the ECR repository, each env file (name, source, destination) and each TOML, all in mono and wrapping; a segmented control narrows to one environment. The chosen service and environment are kept in the URL.

- [ ] **Step 1: Write the failing tests**: environments table with service counts; services search and "No service matches"; selecting a service shows its blocks; narrowing to one environment; a rejected commit shows the warning and problems while the old data stays visible; no source configured shows the empty state; "Sync from git" only with `manage_connections`; loading and error states.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test`. Expected: pass.
- [ ] **Step 5: Commit** `feat(web): add the Inventory screen`.

---

### Task 8: Admin and CLI tokens

Four independent halves; they may go to separate implementers: (a) users and groups, (b) grants and single sign-on, (c) connections and sources, (d) audit log and tokens.

**Files:**
- Create: `web/src/features/admin/AdminPage.tsx`, `UsersSection.tsx`, `GroupsSection.tsx`, `SSOSection.tsx`, `AWSAccountsSection.tsx`, `GitHubSection.tsx`, `SourcesSection.tsx`, `AuditSection.tsx`, `web/src/features/tokens/TokensPage.tsx`, `ConfirmDevicePage.tsx`
- Test: one test file per section

Sections, as tabs of `/admin/:section`, each visible only with its permission:

| Section | Needs | Shows and does |
|---|---|---|
| Users | `manage_access` | Table: name, email, sign-in kind, groups, last sign-in, state. Create a local or service user (name, email, password, kind). Disable and enable, with a confirmation that says their sessions and tokens stop. Edit manual group membership. A 409 for the last admin is shown as "At least one enabled admin must remain" |
| Groups and permissions | `manage_access` | Each group: name, admin switch, members count, and its grants as rows (action, label or environment, folder). Add a grant with selects that only offer valid combinations (the four global actions offer no scope). Remove a grant. Identity-provider group mappings: add, change, remove |
| Single sign-on | `manage_access` | Enabled switch, metadata (paste XML), entity id, attribute names, the service-provider metadata URL to copy, "Test" showing the result |
| AWS accounts | `manage_connections` | Table: name, account id, how discovery connects, how deployment connects, used by which environments. "Connect AWS account" form with two credential blocks, each a role ARN with external id or an access key and secret. "Test" per account |
| GitHub | `manage_connections` | Table of connections; connect by token or by App id, installation id and private key |
| Git sources | `manage_connections` | The inventory source form; a table of every job's source |
| Audit log | `view_audit` | Newest first: when, who, what, on what, with "Load older" paging |

Tokens page (`/cli`): the CLI usage text from the mockup, the person's tokens (name, created, last used, expires), "Create token" showing the value once with a copy button and the sentence "This is the only time the token is shown", and revoke with confirmation.

Device confirm page (`/cli/confirm?code=`): shows the code, "Confirm this sign-in for the command line", and the result.

- Secret inputs are `type="password"`, never pre-filled, cleared after submit, and no secret is echoed back.

- [ ] **Step 1: Write the failing tests**, per section: lists render from fixtures; each create or change calls the right route with the right body and updates the view; each server error is shown in place; each section is absent without its permission and the mock server saw no request for it (Review Focus 3); the grant form never offers a scope for a global action; disabling a user asks for confirmation; the last-admin 409 message; the token value appears once and is gone after leaving and returning; the AWS form posts both credential blocks and clears the secrets; audit paging requests `before=<last id>`; every list has an empty state (Review Focus 4).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make web-lint web-test web-build`. Expected: pass.
- [ ] **Step 5: Commit** per half, for example `feat(web): add user and group administration`.

---

### Task 9: Serve it from the orchestrator and check against the real backend

**Blocked until** Plan 3 has merged into the branch this one is rebased on. Do not start it earlier.

**Files:**
- Create: `internal/webui/webui.go`, `internal/webui/dist/.gitkeep`, `web/e2e/foundation.spec.ts`, `web/playwright.config.ts`
- Modify: `internal/api` (two small additions below), `internal/app` (mount the UI), `deploy/docker/orchestrator.Dockerfile` (a Node build stage pinned to an exact tag), `Makefile` (`web-build` copies `web/dist` into `internal/webui/dist`; `build` depends on it; `web-e2e`), CI, `README.md`
- Test: `internal/webui/webui_test.go`, `internal/api/*_integration_test.go`

Backend additions:
- `GET /api/v1/auth/methods`, no sign-in needed: `{"password": true, "saml": <enabled>}`.
- `GET /api/v1/jobs/{id}` also returns `source`: the raw YAML text of the last good version. Plan 2 stores only the parsed form, so add a column for the raw text in the next free migration and fill it on sync. `GET /api/v1/inventory/services/{name}` likewise returns `source`.

`internal/webui`:

```go
func Handler() http.Handler // serves the embedded build; unknown paths without a file extension get index.html
```

- Paths under `/api/`, `/saml/`, `/healthz` and `/readyz` never reach it.
- Files under `assets/` are served with a one-year immutable cache header; `index.html` with `no-cache`.
- Responses carry `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `X-Frame-Options: DENY`, and a Content-Security-Policy that allows scripts, styles, fonts and connections only from the same origin.
- When no build is embedded, it answers 503 with the text "The web application is not built. Run make web-build."

- [ ] **Step 1: Write the failing Go tests**: `TestServesIndexForAppRoutes`; `TestDoesNotShadowAPI`; `TestAssetCachingAndSecurityHeaders`; `TestReports503WithoutBuild`; `TestAuthMethodsReflectsSSOSetting`; `TestJobAndServiceIncludeSource`.
- [ ] **Step 2: Write the failing browser test** `foundation.spec.ts`, run by `make web-e2e` against an orchestrator started by the test setup on the Compose PostgreSQL and Valkey with a fake GitHub and a created admin: sign in; the Jobs screen lists the registered job under its folder; open it and see its steps and YAML; Inventory shows five environments and a service's blocks; create a group and a grant in Admin and see the audit entry; sign out. Chromium only.
- [ ] **Step 3: Run**; confirm failure.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** `make lint build test test-integration test-e2e web-lint web-test web-e2e smoke`. Expected: all pass, and the smoke stack serves the sign-in page at `/`.
- [ ] **Step 6: Commit** `feat: serve the web application from the orchestrator`.
- [ ] **Step 7: Push, open a pull request, confirm CI passes.** Do not merge it.

---

## Parallel execution

Task 1 first. Then Tasks 2 and 3 together. Then Task 4. Then Tasks 5, 6, 7 and the four halves of Task 8 all together. Task 9 last, when unblocked.

## Definition of done

- `make web-lint web-test web-build` passes locally and in CI after Tasks 1 to 8; the full command in Task 9 Step 5 passes after Task 9.
- Every named test exists, was seen failing first, and passes.
- No dependency outside the allowed lists.
- A reviewer compares each screen with the mockup and confirms the five Review Focus conditions from the named tests.
