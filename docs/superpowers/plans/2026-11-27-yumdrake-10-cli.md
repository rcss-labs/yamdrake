# Yumdrake Plan 10: Command-Line Tool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Everything a person can do with runs in the browser can be done with `yumdrake` from a terminal or a pipeline: sign in, list jobs and versions, plan, run and watch, cancel, approve, restore, with exit codes a script can rely on.

**Architecture:** The existing `cmd/yumdrake` binary (which has `validate` and `version`) gains subcommands that call the same `/api/v1` the web app uses, through one small client package. Output is plain text for people and JSON for scripts.

**Tech Stack:** Go standard library only (`flag`, `net/http`, `encoding/json`). No new module.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 7.6 last bullet, 9.1, 13). API contract: the route tables of Plans 3 to 7, preferring the real handlers.

**Builds on:** Plans 1 to 7.

No full code, by the owner's rule.

## Global Constraints

- All earlier Global Constraints apply. No new module, no CLI framework.
- Exit codes, the same for every command: 0 success (for `run` and `runs watch`: the run `succeeded`); 1 the run finished `failed`; 2 usage or configuration error; 3 `partially_succeeded`; 4 `cancelled`, `rejected`, `expired` or `invalidated`; 5 the run needs a person (`waiting_approval`, `needs_reconciliation`, or `planned` without `--yes`); 6 not signed in or not permitted; 7 the server could not be reached or answered 5xx.
- The token is stored only in the config file, created with mode `0600` in a directory with mode `0700`. It is never printed, never accepted as a command-line argument (flags are visible to other users of the machine), and may come from the `YUMDRAKE_TOKEN` environment variable for pipelines.
- `-o json` on any command prints exactly one JSON document on stdout and nothing else there. Progress and messages go to stderr.
- Text output has no colour unless stdout is a terminal and `NO_COLOR` is unset.
- Every command has `-h` help with one example.
- Server: from `--server`, else `YUMDRAKE_SERVER`, else the config file.

## Review Focus

1. A pipeline runs `yumdrake run ... --yes --watch` and the run needs approval: the command says so, prints the run's link, and exits 5 instead of hanging forever. With `--wait-for-approval` it waits up to the approval's expiry. (Task 3)
2. The connection drops while watching: the command reconnects and continues without repeating lines; after 2 minutes without a connection it exits 7 and says the run is still going. (Task 3)
3. The token is expired or revoked: every command says "Not signed in. Run yumdrake login." and exits 6, with no stack trace and no token in the message. (Task 1)
4. Ctrl-C while watching: the command stops watching and says the run continues, with the command to cancel it. It never cancels the run by itself. (Task 3)
5. A typo in a group or service name in `--set` or `--only`: the error names the bad name and lists the valid ones, before anything is created. (Task 3)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/cli/client.go` | HTTP client: auth header, errors to exit codes, JSON |
| `internal/cli/config.go` | Read and write the config file |
| `internal/cli/output.go` | Tables, JSON, colour rules |
| `internal/cli/sse.go` | Read a run's event stream with resume |
| `cmd/yumdrake/*.go` | One file per command group: `login.go`, `jobs.go`, `run.go`, `runs.go`, `approvals.go`, `restore.go` |
| `test/e2e/cli_floci_test.go` | The built binary against the real system |

---

### Task 1: Client, config and sign-in

**Files:** `internal/cli/client.go`, `config.go`, `output.go`, `cmd/yumdrake/login.go`
**Test:** `internal/cli/*_test.go`, `cmd/yumdrake/login_test.go`

**Interfaces:**

```go
type Config struct{ Server, Token string }
func LoadConfig(getenv func(string) string, path string) (Config, error) // path default: $XDG_CONFIG_HOME/yumdrake/config.json, else ~/.config/yumdrake/config.json
func SaveConfig(path string, c Config) error
type Client struct{ /* server, token, http */ }
func NewClient(c Config) (*Client, error)
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error
type ExitError struct{ Code int; Message string }
func ExitCodeFor(err error) int
```

Commands:
- `yumdrake login [--server URL]`: starts the device sign-in, prints the verification URL and code, opens the browser when it can, polls until confirmed, saves the token, prints "Signed in as <email>". `yumdrake login --with-token` reads a token from stdin instead.
- `yumdrake logout`: revokes the token on the server and removes it from the config.
- `yumdrake whoami`: name, email, and permissions in words.

- [ ] **Step 1: Write the failing tests** against an `httptest` server: config precedence (flag, environment, file); file and directory modes; a config file readable by others is refused with a message; error mapping table to exit codes (401 and 403 to 6, 5xx and connection refused to 7, 404 and 409 and 422 to 2 with the server's message); the token never appears in any error (Review Focus 3); device login happy path, expiry, and polling at the server's interval; `--with-token` from stdin; logout; `-o json` prints one document.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint build test`; expect pass.
- [ ] **Step 5: Commit** `feat(cli): add sign-in, configuration and the API client`.

---

### Task 2: Reading commands

**Files:** `cmd/yumdrake/jobs.go`, `runs.go` (list and show parts)
**Test:** `cmd/yumdrake/jobs_test.go`, `runs_test.go`

- `yumdrake jobs list [--folder NAME]`: id, title, folder, services, approval, last run.
- `yumdrake jobs show JOB`: groups with services, steps with dependencies, problems if any.
- `yumdrake versions JOB --env ENV [--group GROUP | --service SERVICE]`: the common tags per group, or one service's tags, newest first.
- `yumdrake versions running [--service NAME] [--env ENV]`: the running-versions matrix or a slice of it (requires Plan 9's endpoint; if it answers 404 the command says the server does not support it).
- `yumdrake runs list [--job JOB] [--env ENV] [--state STATE]... [--limit N]`.
- `yumdrake runs show RUN`: state, facts, per-step and per-service states, failures with full reasons, wait reason.
- `yumdrake runs log RUN [--step S] [--service S] [--level L] [--grep TEXT]`: prints the stored log through all pages.
- `yumdrake approvals list`.

`JOB` is the job id from the YAML; the client resolves it to the server's numeric id.

- [ ] **Step 1: Write the failing tests**: each command's text output for a fixture and its JSON; unknown job id lists nearby ids; `runs log` follows the cursor to the end and reports gaps on stderr; states filter repeats; help text has an example.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint build test`; expect pass.
- [ ] **Step 5: Commit** `feat(cli): list and show jobs, versions, runs and approvals`.

---

### Task 3: Planning, running and watching

**Files:** `internal/cli/sse.go`, `cmd/yumdrake/run.go`, extend `runs.go`
**Test:** `internal/cli/sse_test.go`, `cmd/yumdrake/run_test.go`

Commands:
- `yumdrake plan JOB --env ENV [--set GROUP=VERSION]... [--set GROUP.SERVICE=VERSION]... [--only NAME[,NAME]...]`: creates a planned run, waits for the plan, prints it (version changes, config changes with key names, checks, fallback warnings) and the run number. Exit 0 if every check passed, 1 if one failed.
- `yumdrake run JOB --env ENV [same selection flags] [--yes] [--watch] [--wait-for-approval]`: plans, prints the plan, asks "Start this run? [y/N]" unless `--yes` (without a terminal and without `--yes` it exits 5 leaving the run planned), starts it, and with `--watch` follows it.
- `yumdrake runs start RUN`, `yumdrake runs watch RUN`, `yumdrake runs cancel RUN [--yes]`.

Selection rules: `--set GROUP=VERSION` selects the whole group at that version; `--set GROUP.SERVICE=VERSION` overrides one service; `--only` narrows the selection to the named groups or services. A group with no `--set` takes its newest common version only if `--latest` is given; otherwise it is an error naming the group. Names are checked against the job before anything is created. (Review Focus 5)

Watching:
- Prints a line per state change of the run and of each leg (`roll-backends / billing: failed: health checks failed`), and with `--logs` every log line. Ends when the run is final, printing the summary and exiting with the code for its state.
- Reconnects with the last event id; gives up after 2 minutes without a connection. (Review Focus 2)
- On `waiting_approval`: prints who may approve and the link; exits 5 unless `--wait-for-approval`. On `needs_reconciliation`: prints the legs and the link; exits 5. (Review Focus 1)
- Ctrl-C prints "Stopped watching. The run continues: yumdrake runs watch N. To cancel it: yumdrake runs cancel N" and exits 130. (Review Focus 4)

- [ ] **Step 1: Write the failing tests**: selection parsing table including overrides, `--only`, `--latest` and every error (unknown group, unknown service, missing version) with the valid names listed (Review Focus 5); `plan` output and exit code on a failed check; `run` without a terminal and without `--yes` exits 5 and starts nothing; `run --yes --watch` exit code for each final state (table over the eight); approval path exits 5 and `--wait-for-approval` continues (Review Focus 1); stream resume without repeats and the 2-minute give-up with a shortened clock (Review Focus 2); interrupt handling (Review Focus 4); `cancel` asks unless `--yes`.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint build test`; expect pass.
- [ ] **Step 5: Commit** `feat(cli): plan, run, watch and cancel from the terminal`.

---

### Task 4: Approving, restoring and re-running

**Files:** `cmd/yumdrake/approvals.go`, `restore.go`
**Test:** matching test files

- `yumdrake approvals approve RUN [--comment TEXT] [--yes]`: shows the plan first, then asks; sends the manifest id it showed. `yumdrake approvals reject RUN [--comment TEXT]`.
- `yumdrake restore RUN [--service NAME]... [--yes] [--watch]`: creates the restore run, prints its plan, asks, starts.
- `yumdrake runs rerun-failed RUN [--yes] [--watch]`.
- `yumdrake runs reconcile RUN --leg ID --outcome succeeded|failed [--yes]` and `yumdrake runs check-again RUN --leg ID`.

- [ ] **Step 1: Write the failing tests**: approve sends the manifest id shown and reports "The plan changed" on a 409; reject; restore prints the plan and warnings about config that will not be restored; rerun-failed selection; reconcile validation and confirmation.
- [ ] **Step 2: Run**; confirm failure. **Step 3: Implement.** **Step 4: Run** `make lint build test`; expect pass.
- [ ] **Step 5: Commit** `feat(cli): approve, restore, re-run and reconcile`.

---

### Task 5: End-to-end check and distribution

**Files:** `test/e2e/cli_floci_test.go`; modify `Makefile` (`cli-dist` cross-compiles for linux and darwin on amd64 and arm64 and windows on amd64 into `dist/`, with checksums), `README.md` and the web app's CLI page text if a command name differs

- [ ] **Step 1: Write the failing test**: build `bin/yumdrake`; against the real system on Floci, as a service account with a token from the environment: `jobs list`, `versions`, `plan` (exit 0), `run --yes --watch` to a good version (exit 0) and to a crashing version (exit 3 with the failed service on stderr), `runs log --service` shows the error line, `restore --yes --watch` (exit 0), `approvals approve` as a second account on a `prod` run that the first command reported with exit 5. Every `-o json` output parses.
- [ ] **Step 2: Run** `make test-e2e`; confirm failure. **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint build test test-integration test-floci test-e2e cli-dist`; expect pass and five archives with a checksum file.
- [ ] **Step 5: Commit** `test(cli): prove the CLI against the real system`.
- [ ] **Step 6: Push, open a pull request, confirm CI passes.** Do not merge it.

---

## Parallel execution

Task 1 first. Then Tasks 2, 3 and 4 together. Then Task 5.

## Definition of done

- The command in Task 5 Step 4 passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- No new module.
- A reviewer confirms the five Review Focus conditions from the named tests.
