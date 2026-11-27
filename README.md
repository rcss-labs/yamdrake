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
export YUMDRAKE_PUBLIC_URL=http://localhost:8080                  # orchestrator only; the address the browser uses

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

The orchestrator serves a JSON API under `/api/v1`. Every request is made by a signed-in user: either a session cookie (from `POST /api/v1/auth/login`, used by the browser) or `Authorization: Bearer <token>` (used by the CLI). See [Signing in](#signing-in) and [CLI tokens and device sign-in](#cli-tokens-and-device-sign-in).

Errors look like `{"error":{"code":"...","message":"..."}}` with codes `unauthorized`, `forbidden`, `not_found`, `invalid`, `conflict`, `too_many_requests`, `unavailable` and `internal`. Request bodies are limited to 1 MB and unknown JSON fields are rejected. No response contains a token, a private key or ciphertext.

| Method and path | Does |
| --- | --- |
| `GET /api/v1/inventory` | Active commit and activation time, last check time, the sync error and its time, a newer rejected commit with its problems, environments, and services with the environments they exist in. |
| `GET /api/v1/inventory/services/{name}` | The full parsed service. |
| `PUT /api/v1/admin/inventory-source` | Set `repo`, `branch` and `path` of the inventory, then sync once. |
| `POST /api/v1/inventory/sync` | Sync the inventory now. |
| `GET /api/v1/folders`, `POST /api/v1/folders` | List folders; create one by `name`. |
| `GET /api/v1/jobs` | Every job source with its folder, commit, sync and check times, runnable flag, problem count and sync error. |
| `POST /api/v1/auth/login`, `POST /api/v1/auth/logout` | Sign in with `email` and `password` (sets the session cookie) and sign out. After 5 failed attempts for one email within 15 minutes, sign-in answers `429` with `Retry-After` until the window passes. |
| `GET /api/v1/me` | The signed-in user: id, email, name, admin flag and grants. |
| `POST /api/v1/jobs` | Register a job file (`folder_id`, `repo`, `branch`, `path`), sync it once and return it. |
| `GET /api/v1/jobs/{id}` | The source, its last good `definition` and its current `problems`. |
| `PATCH /api/v1/jobs/{id}` | Move to another folder (`folder_id`). |
| `POST /api/v1/jobs/{id}/sync` | Sync that job now. |
| `GET /api/v1/admin/github-connections`, `POST /api/v1/admin/github-connections` | List connections without secrets; create one for an `owner` (stored in lower case) with `method` `token` (and `token`) or `app` (and `app_id`, `installation_id`, `private_key`). The secret is encrypted before it is stored. |

```sh
curl -H "Authorization: Bearer $YUMDRAKE_TOKEN" localhost:8080/api/v1/inventory
```

The orchestrator also polls GitHub every `YUMDRAKE_SYNC_INTERVAL` (inventory first, then each job source). A failure to reach GitHub is kept as `sync_error` next to the last good data, which stays in use.

## Signing in

Every `/api/v1` route needs a signed-in user or a CLI token. Without one the API answers `401` with code `unauthorized`. Without the needed permission it answers `403` with code `forbidden`.

### First admin

Create the first admin on the orchestrator's command line, with the same environment as the orchestrator. The password is read from standard input, never from an argument or a variable. It must be at least 12 characters. Take it from your secret store rather than typing it into the shell, so it stays out of shell history.

```sh
printf '%s\n' "$PASSWORD" | yumdrake-orchestrator create-admin --email admin@example.com --name Admin
```

The command creates a local user in the admin group `Administrators` and prints only `created`. It exits 0 on success, 1 on failure (for example, the email already exists) and 2 on a usage error.

### Unlocking an account

Anyone can keep an account locked by sending 5 wrong passwords within 15 minutes, which includes the break-glass admin. An operator clears the failure counter of one email on the orchestrator's command line, with the same environment as the orchestrator:

```sh
yumdrake-orchestrator unlock --email admin@example.com
```

It prints only `unlocked`, writes an `auth.unlock` audit entry, and exits 0 on success, 1 on failure (for example, no user has that email) and 2 on a usage error. The email is matched ignoring case and surrounding spaces.

### Password sign-in

- `POST /api/v1/auth/login` with `{"email": ..., "password": ...}` sets the `yumdrake_session` cookie (`Secure`, `HttpOnly`, `SameSite=Lax`). The session has an idle timeout of 8 hours and a maximum age of 24 hours.
- `POST /api/v1/auth/logout` ends the session.
- `GET /api/v1/me` shows the signed-in user. `GET /api/v1/auth/methods` shows which sign-in methods are on.
- Only `local` and `service` users can use password sign-in.
- After 5 failed attempts for one email within 15 minutes, sign-in answers `429` with `Retry-After` until the window passes.
- State-changing requests that carry the session cookie must come from the public URL: the `Origin` header must equal `YUMDRAKE_PUBLIC_URL`. A script that signs in with a password must send that `Origin` header. Bearer-token requests do not need it.
- If Valkey is unavailable, cookie sign-in answers `503` with "Sign-in is temporarily unavailable". CLI tokens keep working.

### Rate limiting at the edge

These routes need no sign-in and should be rate-limited at the load balancer or ingress: `POST /api/v1/auth/login`, `POST /api/v1/cli/login`, `POST /api/v1/cli/login/token`, `GET /api/v1/auth/saml/start` and `POST /saml/acs`. Yumdrake limits password sign-in per email and caps concurrent password checks, but has no per-client limit.

## CLI tokens and device sign-in

Tokens look like `ymd_...`. They are shown once and stored only as a SHA-256 hash. They expire (90 days by default, at most 365 days) and can be revoked. A token carries its owner's permissions. Send it as `Authorization: Bearer <token>`.

- `GET /api/v1/tokens` and `POST /api/v1/tokens` (body `{"name": ..., "ttl_days": ...}`) list and create your own tokens. `DELETE /api/v1/tokens/{id}` revokes one. Creating and revoking tokens needs a browser session: a request made with a token answers `403` "Sign in with your browser to manage tokens", so a token cannot renew itself.
- A token of a SAML user works only while that user's last SSO sign-in is at most `YUMDRAKE_TOKEN_SSO_MAX_AGE` old (default 168 hours). After that it answers `401` "Sign in again with SSO: yumdrake login". Tokens of `local` and `service` users are not affected.
- Disabling a user revokes all of that user's tokens; enabling the user again does not bring them back.

Device sign-in for the CLI:

1. `POST /api/v1/cli/login` returns `device_code`, `user_code`, `verify_url` and `interval`.
2. A signed-in user confirms the 8-character `user_code` in the browser (`POST /api/v1/cli/login/confirm`).
3. The CLI polls `POST /api/v1/cli/login/token` with the `device_code`, no faster than every `interval` seconds. It receives a token once.

Codes expire after 10 minutes. A session may make at most 10 confirm attempts per 10 minutes; more answer `429` with `Retry-After`.

## Single sign-on (SAML)

An admin with `manage_access` configures SSO at `GET` and `PUT /api/v1/admin/sso`, and checks it with `POST /api/v1/admin/sso/test`.

Give the identity provider:

- Service-provider metadata: `<YUMDRAKE_PUBLIC_URL>/saml/metadata`
- Assertion consumer service URL (HTTP-POST binding): `<YUMDRAKE_PUBLIC_URL>/saml/acs`
- Entity ID (default `<YUMDRAKE_PUBLIC_URL>/saml/metadata`)

Paste the identity provider's metadata XML into the settings as `idp_metadata_xml`. Yumdrake does not fetch it from a URL.

Attributes: groups come from the `groups` attribute and the display name from `displayName` (both configurable). The email comes from the NameID unless an email attribute is set.

Requirements on the identity provider:

- It must sign the response or the assertion with SHA-256 or stronger. SHA-1 is refused.
- Encrypted assertions are not supported.
- Only sign-ins started from Yumdrake are accepted. Identity-provider-initiated sign-in is refused.

Group mapping: map identity-provider groups to Yumdrake groups at `/api/v1/admin/idp-mappings`. Memberships from the identity provider are replaced at every SSO sign-in. Unmapped groups are ignored, and memberships added by an admin by hand are kept. Sessions last at most 24 hours, so a person removed from a group at the identity provider loses it a day later at the latest.

The service-provider signing key and certificate are generated on first use and stored encrypted.

Keep the local admin from `create-admin` as a break-glass account.

## Access administration

Users, groups, grants, identity-provider mappings and the audit log live under `/api/v1/admin/`. `users`, `groups`, `grants` and `idp-mappings` need `manage_access`; `audit` needs `view_audit`.

Actions: `view`, `view_logs`, `run`, `cancel`, `approve`, `restore`, `manage_jobs`, `manage_connections`, `manage_access` and `view_audit`. Every signed-in user has `view`. A group marked admin has every action.

A grant gives a group one action, optionally limited to a label (`dev`, `preprod` or `prod`) or to one environment, and optionally to one folder.

At least one enabled admin must remain; otherwise the change is refused with `409`.

Every change is written to the append-only audit log, which never contains passwords, tokens or keys.

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
| `YUMDRAKE_PUBLIC_URL` | orchestrator | none (required) | Address users reach the service at, such as `https://yumdrake.example.com`: scheme, host and optional port only. Must be `https://`, except `http://` for `localhost` and `127.0.0.1`. Requests that carry a session cookie and change state must come from this origin. |
| `YUMDRAKE_VALKEY_KEY_PREFIX` | orchestrator | empty | At most 64 of letters, digits, `-`, `_`, `:`. Put in front of the Valkey keys of sessions and sign-in counters; set it only to separate services (or test runs) that share one Valkey. |
| `YUMDRAKE_TOKEN_SSO_MAX_AGE` | orchestrator | `168h` | Go duration of at least `1h`. How recent a SAML user's last SSO sign-in must be for their CLI tokens to work. |
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

The GitHub Actions workflow (`.github/workflows/ci.yml`) runs lint, test, integration and smoke jobs on pull requests and on pushes to `main`.
