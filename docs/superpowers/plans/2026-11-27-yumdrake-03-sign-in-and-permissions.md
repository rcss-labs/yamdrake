# Yumdrake Plan 3: Sign-in and Permissions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every request to Yumdrake is made by a known person or token with checked permissions: SAML single sign-on, local accounts for break-glass and service use, groups and grants managed in the application, CLI tokens, and an audit log.

**Architecture:** A package `internal/auth` owns identity (principal, actions, the permission rule), `internal/session` keeps server-side sessions in Valkey, `internal/store` gains the people-and-access tables, and one middleware resolves a principal from a session cookie or a bearer token for every `/api/v1` route. The temporary bootstrap token from Plan 2 is removed.

**Tech Stack:** Go 1.26; two new modules (see Global Constraints): a SAML service-provider library and `golang.org/x/crypto` for argon2id.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 9, 10 "Approvals" for the self-approval rule, 13, 14)

**Builds on:** Plans 1 and 2. Use the actual names in the code where they differ from what this plan assumes.

No full code, by the owner's rule.

## Global Constraints

- Plan 1 and Plan 2 Global Constraints still apply.
- New third-party modules allowed, each to be confirmed as current and maintained on pkg.go.dev before adding, otherwise stop and escalate:
  - `github.com/crewjam/saml` for the SAML service provider (and its `samlidp` package in tests only).
  - `golang.org/x/crypto` for `argon2`.
- Actions are exactly: `view`, `view_logs`, `run`, `cancel`, `approve`, `restore`, `manage_jobs`, `manage_connections`, `manage_access`, `view_audit`.
- Every signed-in principal has `view`. A global admin has every action everywhere.
- Passwords: argon2id, memory 64 MiB, 3 iterations, parallelism 2, 16-byte salt, 32-byte key, stored as a PHC string. Minimum length 12.
- Session cookie: name `yumdrake_session`, `Secure`, `HttpOnly`, `SameSite=Lax`, path `/`. Idle timeout 8 hours, maximum age 24 hours.
- Tokens: 32 random bytes, shown once as `ymd_` plus base64url; only the SHA-256 is stored.
- The server is the authority. A request without permission gets 403 with code `forbidden`; without identity, 401 with code `unauthorized`. No handler relies on the UI to hide anything.
- Nothing secret is logged, returned, or written to the audit log: no passwords, hashes, tokens, session ids, SAML assertions or keys.
- Every change to people, groups, grants, mappings, connections, sources, jobs and folders writes an audit entry in the same transaction as the change.

## What Plans 1 and 2 actually produced

Use these, not the names this plan's text predicted. The previous plans' ledgers and manager reports under `.superpowers/sdd/` have the full list.

- Migrations 00001 to 00003 exist (Plan 2 used two). **This plan's migration is the next free number, 00004**; `db.LatestVersion()` becomes 4.
- Orchestrator wiring lives in `internal/app`: `app.OrchestratorExtension(cfg)` and `Service.Extend`. `api.New(api.Deps)` builds the API. Add to these; do not rebuild wiring in `cmd/orchestrator`.
- `config.Scrub` and `config.Secret` exist for keeping secrets out of text; use them.
- `store` exposes `ErrNotFound`, `ErrDuplicate`, `ErrInvalidReference`, and a `DBTX` interface. Times written to PostgreSQL are truncated to microseconds by the store; tests should use nanosecond times to prove it.
- Test databases: `internal/testutil/testdb` (`testdb.New(t)`), built under the `integration` or `e2e` tags. `make test-e2e` exists. `test/e2e` has a fake GitHub server (token auth only) and an end-to-end sync test that currently authenticates with `YUMDRAKE_BOOTSTRAP_TOKEN`; Task 5 switches it to sign-in.
- A forced exit on a second signal uses exit code 130.
- Modules so far: `pgx/v5` v5.13.0, `goose/v3` v3.30.2, `valkey-go` v1.0.80, `go.yaml.in/yaml/v3` v3.0.6.

## Review Focus

1. A user removed from a group in the identity provider, or disabled in Yumdrake: their next request after the session's group refresh or disable is refused, and their tokens stop working at once. (Tasks 3, 4, 7)
2. Valkey is unavailable: cookie sign-in answers 503 with a clear message, never grants access, and bearer tokens keep working. (Tasks 3, 6)
3. A state-changing request with a valid session cookie but from another origin (cross-site form post): refused. (Task 3)
4. Repeated wrong passwords for one account: slowed and then locked for a while, without revealing whether the account exists. (Task 5)
5. A SAML response that is unsigned, expired, for another audience, or replayed: refused, with nothing created. (Task 7)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/db/migrations/00004_access.sql` | Users, credentials, groups, memberships, mappings, grants, tokens, audit log |
| `internal/store/users.go`, `groups.go`, `grants.go`, `tokens.go`, `audit.go` | SQL for those tables |
| `internal/auth/auth.go` | Actions, principal, target, the permission rule |
| `internal/auth/password.go` | argon2id hashing and verification |
| `internal/auth/resolve.go` | Build a principal from a user id |
| `internal/session/session.go` | Sessions in Valkey, cookie handling, same-origin check |
| `internal/auth/token.go` | Create and verify CLI tokens |
| `internal/audit/audit.go` | Write audit entries |
| `internal/api/authn.go` | Middleware: resolve the principal, enforce an action |
| `internal/api/login.go` | Local sign-in, sign-out, `/me` |
| `internal/api/saml.go` | SAML metadata, start, assertion consumer |
| `internal/api/access.go` | Admin endpoints for users, groups, mappings, grants, SSO settings, audit |
| `internal/api/tokens.go` | Token and CLI device sign-in endpoints |
| `cmd/orchestrator/admin.go` | `create-admin` subcommand |
| `test/e2e/access_test.go` | End-to-end sign-in and permission checks |

---

### Task 1: Tables and storage

**Files:**
- Create: `internal/db/migrations/00004_access.sql`, `internal/store/users.go`, `internal/store/groups.go`, `internal/store/grants.go`, `internal/store/tokens.go`, `internal/store/audit.go`
- Test: `internal/store/access_integration_test.go`

**Interfaces:**
- Produces (all take `ctx` and the `DBTX` interface from Plan 2):

```go
type User struct{ ID int64; Email, Name, Source string; Disabled bool; LastLoginAt *time.Time; CreatedAt time.Time } // Source: "saml" | "local" | "service"
func UpsertSAMLUser(ctx, db, email, name string, at time.Time) (User, error)
func CreateLocalUser(ctx, db, email, name, source, passwordHash string) (User, error) // ErrDuplicate
func UserByEmail(ctx, db, email string) (User, error); func UserByID(ctx, db, id int64) (User, error)
func PasswordHash(ctx, db, userID int64) (string, error) // ErrNotFound for non-local users
func SetDisabled(ctx, db, userID int64, disabled bool) error
func ListUsers(ctx, db) ([]User, error)

type Group struct{ ID int64; Name string; Admin bool }
func CreateGroup(ctx, db, name string, admin bool) (Group, error); func ListGroups(ctx, db) ([]Group, error); func SetGroupAdmin(ctx, db, id int64, admin bool) error
func SetManualMembership(ctx, db, userID, groupID int64, member bool) error
func ReplaceIDPMemberships(ctx, db, userID int64, groupIDs []int64) error // leaves manual memberships alone
func GroupsOfUser(ctx, db, userID int64) ([]Group, error)
func SetIDPMapping(ctx, db, idpGroup string, groupID int64) error; func DeleteIDPMapping(ctx, db, idpGroup string) error
func ListIDPMappings(ctx, db) (map[string]int64, error)

type Grant struct{ ID, GroupID int64; Action, Label, Environment string; FolderID int64 } // empty string / 0 mean "any"
func CreateGrant(ctx, db, g Grant) (Grant, error) // ErrDuplicate; ErrInvalid for rules below
func DeleteGrant(ctx, db, id int64) error; func ListGrants(ctx, db) ([]Grant, error); func GrantsOfUser(ctx, db, userID int64) ([]Grant, error)

type Token struct{ ID, UserID int64; Name string; CreatedAt time.Time; LastUsedAt, ExpiresAt, RevokedAt *time.Time }
func CreateToken(ctx, db, userID int64, name string, hash []byte, expiresAt time.Time) (Token, error)
func TokenByHash(ctx, db, hash []byte) (Token, error); func TouchToken(ctx, db, id int64, at time.Time) error
func RevokeToken(ctx, db, id, userID int64, at time.Time) error; func ListTokens(ctx, db, userID int64) ([]Token, error)

type AuditEntry struct{ ID int64; At time.Time; ActorID *int64; Actor, Action, TargetType, TargetID string; Detail []byte }
func InsertAudit(ctx, db, e AuditEntry) error
func ListAudit(ctx, db, before int64, limit int) ([]AuditEntry, error) // newest first, keyset paging
var ErrInvalid error
```

Rules enforced in SQL constraints as well as Go:
- Email is unique case-insensitively and stored lower-case.
- A grant's `Action` is one of the ten actions. `Label` is empty or `dev`, `preprod`, `prod`. At most one of `Label` and `Environment` is set. `manage_jobs`, `manage_connections`, `manage_access` and `view_audit` grants must have no label, environment or folder. `view` is never stored as a grant.
- The audit table is append-only: the application role is granted no `UPDATE` or `DELETE` on it in the migration's comments and no store function modifies it.

- [ ] **Step 1: Write the failing tests** (integration, fresh database each):
  - `TestUserEmailIsCaseInsensitivelyUnique`; `TestUpsertSAMLUserUpdatesNameAndLastLogin`.
  - `TestReplaceIDPMembershipsKeepsManualOnes`: user in A (manual) and B (idp); replace idp with [C]; groups are A and C.
  - `TestGrantValidation`, table-driven: unknown action; label `staging`; both label and environment; scoped `manage_access`; action `view`; each is `ErrInvalid`. A valid `run` grant for label `dev` in a folder is stored.
  - `TestGrantsOfUserUnionsGroupGrants`.
  - `TestTokenLifecycle`: create, find by hash, touch, revoke; a revoked token is still found and has `RevokedAt` set; revoking another user's token is `ErrNotFound`.
  - `TestAuditIsNewestFirstWithKeysetPaging`.
- [ ] **Step 2: Run** `make test-integration`; confirm failure for missing functions.
- [ ] **Step 3: Implement** the migration and store files.
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass, and `db.LatestVersion()` is 4.
- [ ] **Step 5: Commit** `feat: store users, groups, grants, tokens and the audit log`.

---

### Task 2: Principal, permission rule and passwords

**Files:**
- Create: `internal/auth/auth.go`, `internal/auth/password.go`, `internal/auth/resolve.go`
- Modify: `go.mod`
- Test: `internal/auth/auth_test.go`, `internal/auth/password_test.go`, `internal/auth/resolve_integration_test.go`

**Interfaces:**
- Produces:

```go
type Action string
const ( View Action = "view"; ViewLogs = "view_logs"; Run = "run"; Cancel = "cancel"; Approve = "approve"; Restore = "restore"
        ManageJobs = "manage_jobs"; ManageConnections = "manage_connections"; ManageAccess = "manage_access"; ViewAudit = "view_audit" )
type Target struct{ Label, Environment string; FolderID int64 } // zero value for actions without a scope
type Principal struct {
    UserID      int64
    Email, Name string
    Admin       bool
    Grants      []store.Grant
    Via         string // "session" | "token"
}
func (p Principal) Can(a Action, t Target) bool
func (p Principal) MayApproveRunStartedBy(userID int64, t Target) bool
func Resolve(ctx context.Context, db store.DBTX, userID int64, via string) (Principal, error) // ErrDisabled for a disabled user
func HashPassword(password string) (string, error) // ErrWeakPassword under 12 characters
func VerifyPassword(hash, password string) (ok bool, err error)
var ErrDisabled, ErrWeakPassword error
```

The permission rule:
- `Admin` is true if any of the user's groups is an admin group. An admin can do everything.
- `View` is always allowed.
- Otherwise allowed if some grant has the same action and every scope the grant sets matches the target: a grant's `Label` equals the target's, its `Environment` equals the target's, its `FolderID` equals the target's. A scope the grant leaves empty matches anything.
- `MayApproveRunStartedBy`: needs `Approve` on the target, and the principal must not be the starter unless the principal is an admin (spec section 10).

- [ ] **Step 1: Write the failing tests**:
  - `TestCan`, table-driven: admin allows all ten actions on any target; a principal with no grants allows only `View`; grant `run` label `dev` allows `Run` on `{dev, hetzner-01, 3}` and refuses `{prod, prod-01, 3}`; grant `run` environment `hetzner-02` refuses `hetzner-01`; grant `run` label `dev` folder 3 refuses folder 4; an unscoped `run` grant allows any target; `run` does not imply `approve` or `cancel`; `view_logs` is refused without a grant.
  - `TestMayApproveRunStartedBy`: own run refused; someone else's allowed; admin's own run allowed; no `approve` grant refused.
  - `TestHashAndVerifyPassword`: round trip true; wrong password false with nil error; the hash starts `$argon2id$v=19$m=65536,t=3,p=2$`; two hashes of one password differ.
  - `TestHashPasswordRejectsShort`; `TestVerifyRejectsMalformedHash` returns an error, not false-with-nil.
  - `TestResolve` (integration): unions grants from two groups; `Admin` true through an admin group; a disabled user is `ErrDisabled`.
- [ ] **Step 2: Run**; confirm failure for the missing package.
- [ ] **Step 3: Add** `golang.org/x/crypto` and implement.
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add principals, the permission rule and argon2id passwords`.

---

### Task 3: Sessions

**Files:**
- Create: `internal/session/session.go`
- Test: `internal/session/session_integration_test.go`, `internal/session/origin_test.go`

**Interfaces:**
- Consumes: Plan 1 `cache` client.
- Produces:

```go
type Session struct{ ID string; UserID int64; CreatedAt, LastSeenAt time.Time; GroupsCheckedAt time.Time }
type Manager struct{ /* unexported */ }
func NewManager(c valkey.Client, now func() time.Time) *Manager
func (m *Manager) Create(ctx context.Context, userID int64) (Session, error)
func (m *Manager) Get(ctx context.Context, id string) (Session, error) // ErrNoSession when absent, idle-expired or past maximum age; refreshes LastSeenAt
func (m *Manager) Delete(ctx context.Context, id string) error
func (m *Manager) DeleteAllForUser(ctx context.Context, userID int64) error
func SetCookie(w http.ResponseWriter, s Session); func ClearCookie(w http.ResponseWriter); func IDFromRequest(r *http.Request) (string, bool)
func SameOrigin(r *http.Request, publicURL string) bool
var ErrNoSession, ErrUnavailable error
```

Behaviour:
- Session ids are 32 random bytes, base64url. Stored under `session:<id>` with the idle timeout as the key's expiry; a per-user set `user-sessions:<userID>` supports `DeleteAllForUser`.
- Any Valkey failure is `ErrUnavailable`, never treated as "no session" and never as a valid one.
- `SameOrigin` is true for `GET`, `HEAD` and `OPTIONS`. For other methods it requires an `Origin` header whose scheme and host equal `publicURL`'s, or, with no `Origin`, a `Sec-Fetch-Site` of `same-origin`. Neither header: false.
- New configuration: `YUMDRAKE_PUBLIC_URL` (orchestrator, required, `https://` unless host is `localhost` or `127.0.0.1`).

- [ ] **Step 1: Write the failing tests**: `TestCreateGetDelete`; `TestIdleTimeoutExpires` (clock advanced 8h1m); `TestMaximumAgeExpiresDespiteActivity` (touch hourly, expires after 24h); `TestDeleteAllForUser` (three sessions, one of another user survives); `TestValkeyDownIsErrUnavailable` (client pointed at a closed port); `TestCookieAttributes`; `TestSameOrigin` table (GET any origin true; POST matching Origin true; POST other Origin false; POST no Origin with `Sec-Fetch-Site: same-origin` true, `cross-site` false, neither false).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add server-side sessions with same-origin protection`.

---

### Task 4: Tokens and audit writer

**Files:**
- Create: `internal/auth/token.go`, `internal/audit/audit.go`
- Test: `internal/auth/token_integration_test.go`, `internal/audit/audit_integration_test.go`

**Interfaces:**
- Produces:

```go
// auth
func NewToken(ctx context.Context, db store.DBTX, userID int64, name string, ttl time.Duration, now time.Time) (plaintext string, t store.Token, err error)
func VerifyToken(ctx context.Context, db store.DBTX, plaintext string, now time.Time) (userID int64, err error) // ErrBadToken for unknown, expired, revoked or malformed
var ErrBadToken error

// audit
type Event struct{ Action, TargetType, TargetID string; Detail map[string]any }
func Record(ctx context.Context, db store.DBTX, actor *auth.Principal, at time.Time, e Event) error // actor nil means "system"
```

Behaviour:
- `VerifyToken` compares in constant time, updates `LastUsedAt` at most once a minute, and returns the same `ErrBadToken` for every failure.
- `ttl` is capped at 365 days; default 90 days when zero.
- `Record` refuses (returns an error and writes nothing) if `Detail` has a key named `password`, `token`, `secret`, `private_key`, `hash` or `assertion`, at any depth. This is a guard against mistakes, tested.

- [ ] **Step 1: Write the failing tests**: `TestNewTokenFormatAndOnlyHashStored`; `TestVerifyToken` table (valid; unknown; malformed without prefix; expired; revoked: all the same error); `TestTTLDefaultAndCap`; `TestRecordWritesActorAndDetail`; `TestRecordSystemActor`; `TestRecordRefusesSecretKeys` (top level and nested).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add CLI tokens and the audit writer`.

---

### Task 5: Middleware, local sign-in and the first admin

**Files:**
- Create: `internal/api/authn.go`, `internal/api/login.go`, `cmd/orchestrator/admin.go`
- Modify: `internal/api/api.go` and `internal/api/guard.go` (remove the bootstrap token), `internal/api/definitions.go` (attach an action to every Plan 2 route), `internal/config/config.go` (remove `YUMDRAKE_BOOTSTRAP_TOKEN`, add `YUMDRAKE_PUBLIC_URL`), `cmd/orchestrator/main.go`, `test/e2e/sync_test.go` (sign in instead of using the token)
- Test: `internal/api/authn_integration_test.go`, `internal/api/login_integration_test.go`, `cmd/orchestrator/admin_integration_test.go`

**Interfaces:**
- Produces:

```go
func (a *API) authenticate(next http.Handler) http.Handler            // resolves a principal into the request context or answers 401 / 503
func PrincipalFrom(ctx context.Context) (auth.Principal, bool)
func (a *API) require(action auth.Action, target func(*http.Request) (auth.Target, error)) func(http.Handler) http.Handler
```

Routes added:

| Method and path | Does |
|---|---|
| `POST /api/v1/auth/login` | Email and password. On success sets the cookie and returns the principal |
| `POST /api/v1/auth/logout` | Deletes the session, clears the cookie |
| `GET /api/v1/me` | The principal: id, email, name, admin, grants |

Actions for Plan 2's routes: every `GET` under `/api/v1/inventory`, `/jobs`, `/folders` needs `view`; `POST /jobs`, `PATCH /jobs/{id}`, `POST /jobs/{id}/sync`, `POST /folders` need `manage_jobs`; `PUT /admin/inventory-source`, `POST /inventory/sync` and both `/admin/github-connections` routes need `manage_connections`. Each of those writes now records an audit event in the same transaction.

Behaviour:
- `authenticate` order: an `Authorization: Bearer` header is tried as a token; otherwise the session cookie. A bearer request ignores cookies. Cookie requests must pass `session.SameOrigin`, else 403 `forbidden`.
- Session Valkey failure: 503 with code `unavailable` and message "Sign-in is temporarily unavailable". Token requests are unaffected. (Review Focus 2)
- A disabled user: 401, and for a session, the session is deleted.
- Sign-in: wrong email and wrong password give the same 401 and take the same time (verify against a fixed dummy hash when the user does not exist). After 5 failures for one email within 15 minutes, further attempts for that email get 429 with `Retry-After`, without checking the password, until the window passes. Counters live in Valkey; if Valkey is down, sign-in answers 503. (Review Focus 4)
- Only users with source `local` or `service` can use password sign-in.
- Successful and failed sign-ins and sign-outs are audited (the failed one with the email attempted, never the password).
- `yumdrake-orchestrator create-admin --email <e> --name <n>` reads a password from stdin (not from an argument or environment variable), creates a local user in an admin group named `Administrators` (creating the group if needed), audits it, and prints only "created". It refuses if the email exists. Exit 0, 1 on failure, 2 on usage.

- [ ] **Step 1: Write the failing tests**:
  - `TestNoCredentialsIs401`; `TestBearerTokenAuthenticates`; `TestRevokedTokenIs401`; `TestSessionCookieAuthenticates`.
  - `TestCookiePostFromOtherOriginIsForbidden`. (Review Focus 3)
  - `TestDisabledUserIsRefusedAndSessionDeleted`; `TestDisabledUsersTokenStopsAtOnce`. (Review Focus 1)
  - `TestValkeyDownCookieIs503AndTokenStillWorks`. (Review Focus 2)
  - `TestRequireRefusesWithoutGrantAndAllowsWithIt`: a user with no grants gets 403 on `POST /api/v1/folders` and 200 on `GET /api/v1/jobs`; after a `manage_jobs` grant, the `POST` succeeds and an audit entry exists.
  - `TestBootstrapTokenNoLongerWorks`.
  - `TestLoginSuccessSetsCookieAndAudits`; `TestLoginWrongPasswordAndUnknownEmailLookTheSame` (same status and body; durations within 50% of each other over 5 tries); `TestLoginLocksAfterFiveFailures` (sixth is 429 with `Retry-After`, a correct password during the lock is still 429, and it works after the window); `TestSAMLUserCannotPasswordLogin`; `TestLogoutDeletesSession`.
  - `TestCreateAdminReadsPasswordFromStdin`; `TestCreateAdminRefusesExistingEmail`; `TestCreateAdminRejectsShortPassword`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement**, remove the bootstrap token everywhere, and update the Plan 2 end-to-end test to create an admin and sign in.
- [ ] **Step 4: Run** `make lint build test test-integration test-e2e`. Expected: pass.
- [ ] **Step 5: Commit** `feat: authenticate every API request and add local sign-in`.

---

### Task 6: Access administration, tokens and CLI device sign-in

**Files:**
- Create: `internal/api/access.go`, `internal/api/tokens.go`
- Test: `internal/api/access_integration_test.go`, `internal/api/tokens_integration_test.go`

Routes:

| Method and path | Needs | Does |
|---|---|---|
| `GET /api/v1/admin/users` | `manage_access` | List users with their groups |
| `POST /api/v1/admin/users` | `manage_access` | Create a `local` or `service` user with a password |
| `PATCH /api/v1/admin/users/{id}` | `manage_access` | Disable or enable; set manual group memberships |
| `GET`, `POST /api/v1/admin/groups`; `PATCH /api/v1/admin/groups/{id}` | `manage_access` | List, create, set admin flag |
| `GET`, `PUT`, `DELETE /api/v1/admin/idp-mappings/{idpGroup}` | `manage_access` | Map an identity-provider group to a Yumdrake group |
| `GET`, `POST /api/v1/admin/grants`; `DELETE /api/v1/admin/grants/{id}` | `manage_access` | Manage grants |
| `GET /api/v1/admin/audit?before=&limit=` | `view_audit` | Page through the audit log, newest first, limit at most 200 |
| `GET`, `POST /api/v1/tokens`; `DELETE /api/v1/tokens/{id}` | signed in | The caller's own tokens. The plaintext is returned once, on creation |
| `POST /api/v1/cli/login` | none | Start a device sign-in: returns `device_code`, `user_code`, `verify_url`, `interval` |
| `POST /api/v1/cli/login/confirm` | signed in (session) | The browser confirms a `user_code` |
| `POST /api/v1/cli/login/token` | none | The CLI polls with `device_code`: `pending`, or a new token once confirmed |

Behaviour:
- Disabling a user deletes all their sessions. Tokens stop because `Resolve` refuses a disabled user.
- An admin cannot remove the admin flag from, or disable, the last remaining way to administer: at least one enabled user in an admin group must remain, else 409 `conflict`.
- Device sign-in state lives in Valkey for 10 minutes. `user_code` is 8 characters from an unambiguous alphabet. `device_code` is 32 random bytes. A `device_code` yields a token exactly once. Polling faster than `interval` (5s) answers 429.
- Every change is audited with the before and after values that are safe to record.

- [ ] **Step 1: Write the failing tests**: `TestNonAdminCannotManageAccess`; `TestCreateGroupGrantAndMembershipThenUserGainsPermission`; `TestDisableUserDeletesSessions`; `TestCannotRemoveLastAdmin` (disable and un-admin paths); `TestIDPMappingCRUD`; `TestAuditPagingAndPermission`; `TestTokenPlaintextReturnedOnceAndListNeverShowsIt`; `TestUserCannotRevokeAnotherUsersToken`; `TestDeviceLoginHappyPath`; `TestDeviceCodeYieldsTokenOnce`; `TestDeviceLoginExpires`; `TestDevicePollTooFastIs429`; `TestEveryChangeIsAudited` (one assertion per mutating route above).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add access administration, tokens and CLI device sign-in`.

---

### Task 7: SAML single sign-on

**Files:**
- Create: `internal/api/saml.go`, `internal/auth/samlsettings.go`
- Modify: `go.mod`, `internal/api/access.go` (settings routes)
- Test: `internal/api/saml_integration_test.go`

**Interfaces:**
- Produces:

```go
type SAMLSettings struct {
    Enabled           bool
    IDPMetadataXML    string // fetched once from a URL by the admin, or pasted; stored, not re-fetched per sign-in
    EntityID          string // default: <public URL>/saml/metadata
    GroupsAttribute   string // default "groups"
    EmailAttribute    string // empty means use NameID
    NameAttribute     string // default "displayName"
}
func LoadSAMLSettings(ctx, db) (SAMLSettings, error); func SaveSAMLSettings(ctx, db, s SAMLSettings) error
```

Routes:

| Method and path | Does |
|---|---|
| `GET /saml/metadata` | Service-provider metadata XML |
| `GET /api/v1/auth/saml/start` | Redirects to the identity provider with a signed request and a relay state bound to a short-lived cookie |
| `POST /saml/acs` | Assertion consumer. Validates, signs the user in, redirects to `/` |
| `GET`, `PUT /api/v1/admin/sso` (`manage_access`) | Read and change the settings; `PUT` validates the metadata parses |
| `POST /api/v1/admin/sso/test` (`manage_access`) | Checks the stored metadata parses and has a signing certificate and a redirect or POST sign-on endpoint |

Behaviour:
- The service provider's signing key and certificate are generated on first use (RSA 2048, self-signed, 10 years), encrypted with `secrets.Cipher` under purpose `saml-sp-key`, and stored in `instance_settings`.
- Accepted only if: the response or assertion is signed by the identity provider's certificate; it is within its validity window (allowing 60s clock skew); the audience is our entity id; the destination is our ACS URL; it answers a request we made (the relay-state cookie matches); its assertion id has not been seen before (remember ids in Valkey until the assertion expires). Anything else: 400, an audit entry `saml.rejected` with the reason, and no user or session created. (Review Focus 5)
- On success: upsert the user by email with source `saml`; replace their identity-provider memberships from the groups attribute through the mappings (unmapped groups are ignored); refuse a disabled user; create a session; audit `auth.login` with `via: saml`.
- A person removed from a group at the identity provider loses it at their next sign-in, which the 24-hour maximum session age forces. (Review Focus 1)
- `/saml/acs` is exempt from the same-origin check (the identity provider posts to it) and from nothing else.

- [ ] **Step 1: Write the failing tests** using `samlidp` from the SAML module as an in-process identity provider:
  - `TestMetadataIsServedAndNamesACS`.
  - `TestSSOHappyPathCreatesUserMapsGroupsAndSignsIn`: identity provider user in `eng-devops` and `unmapped`; mapping `eng-devops` to an admin group; after the flow `/api/v1/me` shows admin and the right email.
  - `TestGroupRemovedAtIDPIsRemovedAtNextSignIn`; `TestManualMembershipSurvivesSSO`.
  - `TestRejects`, table-driven: unsigned assertion; signed by another key; expired; wrong audience; wrong destination; no matching relay state; the same response posted twice. Each: 400, no session cookie, no new user row, one `saml.rejected` audit entry. (Review Focus 5)
  - `TestDisabledUserCannotSSO`; `TestSPKeyIsStoredEncryptedAndReused`; `TestSSOSettingsValidation` (garbage metadata is `invalid`).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Add** the SAML module and implement.
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add SAML single sign-on with group mapping`.

---

### Task 8: End-to-end check

**Files:**
- Create: `test/e2e/access_test.go`
- Modify: `scripts/smoke.sh`, `compose.yaml` (new required variables), `README.md` (first admin, SSO setup)

- [ ] **Step 1: Write the failing test** `TestAccessEndToEnd`: start the orchestrator in-process. Run `create-admin`. Sign in as the admin. Create group `QA`, grant it `run` for label `dev`, create a local user in it. As that user: `GET /api/v1/jobs` is 200, `POST /api/v1/folders` is 403, `GET /api/v1/admin/audit` is 403. Create a token for that user through the device flow and repeat one allowed and one refused call with it. Disable the user as admin: the cookie and the token are both refused. The audit log, read as admin, contains entries for each of those changes in order and no secret values (search the JSON for the password and the token plaintext).
- [ ] **Step 2: Run** `make test-e2e`; confirm failure.
- [ ] **Step 3: Implement** the test and update the smoke script, Compose and README.
- [ ] **Step 4: Run** `make lint test test-integration test-e2e smoke`. Expected: all pass.
- [ ] **Step 5: Commit** `test: add end-to-end sign-in and permission test`.
- [ ] **Step 6: Push the branch, open a pull request against the previous plan's branch, confirm CI passes.** Do not merge it.

---

## Parallel execution

Task 1 first. Then Tasks 2, 3 and the audit half of Task 4 together (Task 4's token half needs Task 1 only). Then Task 5. Then Tasks 6 and 7 together. Then Task 8.

## Definition of done

- `make lint test test-integration test-e2e smoke` passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- Exactly two new third-party modules.
- `YUMDRAKE_BOOTSTRAP_TOKEN` no longer exists anywhere in the repository.
- A reviewer confirms the five Review Focus conditions from the named tests.
