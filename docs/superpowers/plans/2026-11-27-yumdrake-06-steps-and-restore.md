# Yumdrake Plan 6: Deployment Steps and Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Yumdrake really deploys: it writes the manifest's config files to their destinations, rolls each ECS service onto the pinned image and confirms the intended revision is the one running, survives a runner dying at any point, and can restore a service to exactly what it ran before. Proven against Floci.

**Architecture:** Two executors, `copy-config` and `ecs-deploy`, implement Plan 4's `runner.Executor` and act only on what the manifest pins. Every external change goes through `Env.Do` so a second attempt can recognise and adopt the first attempt's work. Restore is a run whose manifest points at the recorded previous state.

**Tech Stack:** Go 1.26, AWS SDK for Go v2 (already present), Floci 2.5.0 with the test proxy from Plan 5.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 4 D3, 7.8, 7.9, 8.1, 8.2, 8.3, 16). Floci findings: `docs/research/2026-11-27-floci.md`.

**Builds on:** Plans 1 to 5. Use the actual names in the code where they differ.

No full code, by the owner's rule.

## Global Constraints

- All earlier plans' Global Constraints still apply. No new third-party module.
- An executor acts only on the manifest: digests, stored config copies, the previous task definition. It never re-reads a tag, a branch or a config source.
- Every external change is made inside `Env.Do` with a key that is stable across attempts.
- Yumdrake does not roll back. A failed service is left as ECS leaves it.
- An `ecs-deploy` leg succeeds only if, at the end, the service's primary deployment uses the task definition this leg applied and running count equals desired count.
- A task definition registered by Yumdrake carries the tags `yumdrake:version`, `yumdrake:run`, `yumdrake:leg` and `yumdrake:manifest`, and keeps every tag the previous revision had.
- Defaults: rollout poll every 5s, rollout timeout 15 minutes unless the step sets `timeout`, a leg becomes `unknown` after 2 minutes of consecutive failures to read the service's state.
- Failure signals that Floci cannot produce are tested with stub clients and with the proxy's `Rewrite`.

## Review Focus

1. The runner dies after `RegisterTaskDefinition` succeeded and before it was recorded: the next attempt finds the tagged revision and uses it. Exactly one new revision exists at the end. (Tasks 3, 6)
2. The new version's tasks keep failing: the leg fails with the stopped tasks' reason and exit code in the summary, within the timeout, and never reports success because ECS briefly looked stable. (Tasks 3, 6)
3. ECS rolls the service back by itself: the leg fails and says the service is back on the previous revision. (Task 3)
4. A config write is refused halfway through a service's files: the leg fails, the log says exactly which files were written and which were not, and a later attempt writes only what is missing. (Tasks 2, 6)
5. A restore of a run older than the config retention: it restores the task definition and says plainly that config was not restored, instead of failing or pretending. (Task 4)

## File Structure

| Path | Responsibility |
|---|---|
| `internal/steps/copyconfig/copyconfig.go` | The `copy-config` executor |
| `internal/steps/ecsdeploy/taskdef.go` | Build the new task definition from the previous one |
| `internal/steps/ecsdeploy/rollout.go` | Decide a rollout's state from what ECS reports |
| `internal/steps/ecsdeploy/ecsdeploy.go` | The `ecs-deploy` executor |
| `internal/engine/restore.go` | Build restore and re-run-failed runs |
| `internal/steps/resolve/restore.go` | Resolve a restore run's manifest |
| `internal/api/restore.go` | Restore and re-run endpoints |
| `test/e2e/deploy_floci_test.go` | Real deployments, failures, recovery and restore against Floci |

---

### Task 1: Task definition rewrite

**Files:**
- Create: `internal/steps/ecsdeploy/taskdef.go`
- Test: `internal/steps/ecsdeploy/taskdef_test.go`

**Interfaces:**
- Produces:

```go
type Stamp struct{ Version, ManifestID string; RunID, LegID int64 }
func NextTaskDefinition(prev ecstypes.TaskDefinition, prevTags []ecstypes.Tag, repo awsx.ECRRepoRef, digest string, s Stamp) (*ecs.RegisterTaskDefinitionInput, error)
func ImageRepository(image string) string // the image reference without tag or digest
var ErrNoMatchingContainer error
```

Behaviour:
- Every field of the previous definition that `RegisterTaskDefinition` accepts is carried over unchanged.
- Each container whose `ImageRepository` equals the service's repository URI gets image `<repository>@<digest>`. Other containers (sidecars from other repositories) are untouched. No container matching is `ErrNoMatchingContainer`, naming the repository and the images found.
- Tags: the previous tags, with the four `yumdrake:` tags added or replaced.

- [ ] **Step 1: Write the failing tests**: `TestImageRepository` table (tag form, digest form, tag and digest, registry with port, no tag); `TestNextTaskDefinitionRewritesOnlyMatchingContainers`; `TestNextTaskDefinitionKeepsEverythingElse` (a previous definition with every optional block set: roles, network mode, volumes, placement constraints, compatibilities, cpu, memory, pid and ipc mode, proxy configuration, ephemeral storage, runtime platform, container secrets, environment, health check, log configuration; the input equals it field for field apart from the image); `TestEveryRegisterInputFieldIsCopiedOrDeliberatelyIgnored` (reflect over `ecs.RegisterTaskDefinitionInput`: each exported field is either set by the function for the fully populated fixture or listed in a small explicit ignore list with a reason, so an SDK upgrade that adds a field fails this test); `TestTagsAreMergedAndStamped`; `TestNoMatchingContainerIsError`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement** as pure code.
- [ ] **Step 4: Run** `make lint test`. Expected: pass.
- [ ] **Step 5: Commit** `feat: build the next task definition from the previous one`.

---

### Task 2: The copy-config executor

**Files:**
- Create: `internal/steps/copyconfig/copyconfig.go`
- Test: `internal/steps/copyconfig/copyconfig_test.go`, `internal/steps/copyconfig/copyconfig_floci_test.go`

**Interfaces:**
- Consumes: `runner.Executor`, `runner.Env`, `manifest.Manifest` (from the run), `artifacts.Store`, `deploy.Provider`.
- Produces: `func New(d Deps) runner.Executor`, with `Deps{AWS, Artifacts}`; and the leg result type

```go
type Result struct{ Written, AlreadyInPlace, NotWritten []string } // destination URIs
```

Behaviour, for the leg's one service:
- For each of the service's config items in manifest order: read the destination and hash it. If it already equals the manifest's checksum, record it under `AlreadyInPlace` and log so. Otherwise, inside `Env.Do` (name `put-config`, key the destination URI): fetch the stored copy (checksum verified), `PutObject` to the destination with the deployment credentials of the environment's account, read it back and confirm the hash.
- On `ErrAmbiguous` from `Env.Do`: read the destination; equal to the manifest means done, anything else means write it.
- Between files, if cancel was requested: stop, and return `cancelled` with the result so far.
- A refused or failed write ends the leg `failed` with short reason `<AWS error code> on s3:PutObject` and a result listing every file under `Written`, `AlreadyInPlace` or `NotWritten`. The log states the same in plain words.
- A service with no config items succeeds with the line `no env files or TOMLs for this service, nothing to copy`.
- File contents are never logged.

- [ ] **Step 1: Write the failing unit tests** with stub S3 and artifact store: `TestCopiesEveryItemAndVerifies`; `TestSkipsFilesAlreadyInPlace`; `TestDeniedHalfwayReportsWrittenAndNotWritten` (Review Focus 4); `TestSecondAttemptWritesOnlyMissing` (Review Focus 4); `TestAmbiguousPutIsResolvedByReadingDestination` both ways; `TestCancelBetweenFilesReturnsCancelledWithResult`; `TestCorruptArtifactFailsWithoutWriting`; `TestNoConfigSucceeds`; `TestLogNeverContainsFileContents`.
- [ ] **Step 2: Write the failing Floci test** `TestCopyConfigAgainstFloci`: an env file and a TOML land at their destinations byte for byte.
- [ ] **Step 3: Run**; confirm failure.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** `make lint test test-floci`. Expected: pass.
- [ ] **Step 6: Commit** `feat: add the copy-config step`.

---

### Task 3: Rollout judgement and the ecs-deploy executor

**Files:**
- Create: `internal/steps/ecsdeploy/rollout.go`, `internal/steps/ecsdeploy/ecsdeploy.go`
- Test: `internal/steps/ecsdeploy/rollout_test.go`, `internal/steps/ecsdeploy/ecsdeploy_test.go`, `internal/steps/ecsdeploy/ecsdeploy_floci_test.go`

**Interfaces:**
- Produces:

```go
type Observation struct {
    Service       ecstypes.Service
    StoppedTasks  []ecstypes.Task // tasks of our task definition that stopped since the update, newest first
    Elapsed       time.Duration
    ReadFailures  time.Duration   // how long reads have been failing in a row
}
type Verdict struct{ State, Short, Detail string } // State: "waiting" | "succeeded" | "failed" | "unknown"
func Judge(o Observation, ourTaskDef string, timeout time.Duration) Verdict
func New(d Deps) runner.Executor // Deps{AWS, PollEvery, DefaultTimeout}
type Result struct{ TaskDefinition, PreviousTaskDefinition string; Adopted bool }
```

`Judge`, in this order:
1. Reads failing for 2 minutes or more: `unknown`.
2. Desired count 0 and the service's task definition is ours: `succeeded`, detail `desired count is 0, nothing to wait for`.
3. The service's primary deployment is no longer ours: `failed`, short `ECS rolled back`, detail naming the revision now primary. (Review Focus 3)
4. Our deployment's rollout state is `FAILED`: `failed`, with ECS's reason.
5. Three or more of our tasks have stopped for a reason other than a deployment replacing them: `failed`, short `tasks keep stopping`, detail the newest task's stopped reason, container name and exit code. (Review Focus 2)
6. Rollout state `COMPLETED`, the service's task definition is ours, running equals desired, and no other deployment remains: `succeeded`.
7. Elapsed beyond the timeout: `failed`, short `timed out after <duration>`, detail with running and desired counts and the newest stopped task's reason if any.
8. Otherwise `waiting`.

Executor, for the leg's one service:
1. If the manifest gives this service a `TargetTaskDefinition` (a restore), use it and skip to step 4.
2. Look for a revision of the family tagged with this run and leg (newest ten revisions). Found: adopt it and log so. (Review Focus 1)
3. Otherwise read the manifest's previous task definition with its tags, build the next one (Task 1), and register it inside `Env.Do` (name `register-task-definition`, key the leg id). On `ErrAmbiguous`, repeat step 2 before registering. Checkpoint the ARN.
4. Update the service to that task definition inside `Env.Do` (name `update-service`, key the ARN). On `ErrAmbiguous`: if the service already uses it, continue; otherwise update.
5. Poll: describe the service, list and describe our stopped tasks, log each new service event once, call `Judge`, and stop on anything but `waiting`.
6. A cancel request does not stop the wait: the leg keeps observing and reports the real outcome, logging once that it was asked to cancel.
- On failure the log also lists up to five stopped tasks with reason, container and exit code.
- AWS throttling and transient errors are retried by the SDK; an access-denied on register or update fails the leg at once with the AWS error code and request id.

- [ ] **Step 1: Write the failing tests** for `Judge`: one test per numbered rule, plus `TestJudgeIgnoresTasksStoppedByDeployment`, `TestJudgeDoesNotSucceedWhileAnOlderDeploymentRemains`, `TestJudgeDoesNotSucceedOnBriefStabilityOfAFailingRevision` (rollout `COMPLETED` but three of our tasks already stopped: `failed`, because rule 5 comes first). (Review Focus 2)
- [ ] **Step 2: Write the failing executor tests** with a stub ECS: `TestHappyPathRegistersUpdatesAndSucceeds`; `TestAdoptsTaggedRevisionInsteadOfRegisteringAgain` (Review Focus 1); `TestAmbiguousRegisterLooksForTaggedRevisionFirst`; `TestAmbiguousUpdateChecksServiceFirst`; `TestRestoreUsesTargetTaskDefinitionWithoutRegistering`; `TestAccessDeniedOnRegisterFailsWithCodeAndLeavesServiceAlone`; `TestFailureLogsStoppedTaskReasons`; `TestCancelDuringRolloutKeepsObserving`; `TestReadFailuresForTwoMinutesGiveUnknown`; `TestEachServiceEventIsLoggedOnce`.
- [ ] **Step 3: Write the failing Floci tests**: `TestDeployAgainstFloci` (the service ends on a revision whose image is the pinned digest and which has the four tags and the previous tags); `TestRolloutFailureThroughProxyRewrite` (the proxy rewrites `DescribeServices` so our deployment reports `FAILED`); `TestCrashingImageFailsByStoppedTasks` (an image that exits 3: the leg fails with `tasks keep stopping` and the exit code).
- [ ] **Step 4: Run**; confirm failure.
- [ ] **Step 5: Implement.**
- [ ] **Step 6: Run** `make lint test test-floci`. Expected: pass.
- [ ] **Step 7: Commit** `feat: add the ecs-deploy step with rollout judgement and recovery`.

---

### Task 4: Restore and re-run failed

**Files:**
- Create: `internal/engine/restore.go`, `internal/steps/resolve/restore.go`, `internal/api/restore.go`
- Modify: `internal/manifest/manifest.go` (`Service.TargetTaskDefinition string`; `Manifest.RestoreOf int64`; `ConfigItem.NotRestored string`), `internal/steps/resolve/resolve.go` (dispatch on run kind)
- Test: `internal/engine/restore_integration_test.go`, `internal/steps/resolve/restore_test.go`, `internal/api/restore_integration_test.go`

**Interfaces:**
- Produces:

```go
func (e *Engine) CreateRestore(ctx context.Context, p auth.Principal, sourceRunID int64, services []string, via string) (store.Run, error) // empty services means every service the source run changed
func (e *Engine) RerunSelection(ctx context.Context, p auth.Principal, sourceRunID int64) (jobSourceID int64, envName string, sel Selection, err error)
```

Restore behaviour:
- Needs `restore` on the target. The source run must have a manifest and at least one leg that changed something (`ErrWrongState` otherwise).
- The new run has kind `restore`, `ParentRunID` the source, the same environment, and two generated steps for the chosen services: `restore-config` (use `copy-config`) and `restore-deploy` (use `ecs-deploy`, needing `restore-config` with scope `service`). It never auto-starts: a person looks at its plan and starts it. It follows the same approval rule as the source job.
- Resolving a restore: for each service, the target task definition is the source manifest's `PreviousTaskDefinition`, and each config item's content is the source's previous copy. It records the current state as its own "previous", so a restore can itself be restored.
- A config item that did not exist before the source run is left in place and marked `NotRestored: "did not exist before run #<n>; left in place"`.
- A config item whose previous copy is gone (retention passed) is marked `NotRestored: "config copy expired; only the task definition is restored"`, shown in the plan as a warning, and does not fail the check. (Review Focus 5)
- If the previous task definition no longer exists or is inactive, that is a failed check.

Re-run failed: returns the job, environment and a selection of the source run's failed, skipped and cancelled services with the versions they had. The caller creates a normal run from it, with `ParentRunID` set.

Routes:

| Method and path | Needs | Does |
|---|---|---|
| `POST /api/v1/runs/{id}/restore` | `restore` | Body: optional list of services. Creates the restore run in `resolving`; returns it |
| `POST /api/v1/runs/{id}/rerun-failed` | `run` | Creates a planned run for the failed, skipped and cancelled services |

- [ ] **Step 1: Write the failing tests**: `TestCreateRestoreBuildsTwoStepsForChangedServices`; `TestCreateRestoreNeedsRestorePermission`; `TestRestoreOfRunThatChangedNothingIsRefused`; `TestRestoreManifestTargetsPreviousTaskDefinitionAndPreviousConfig`; `TestRestoreLeavesFilesThatDidNotExistBefore`; `TestRestoreWithExpiredCopiesWarnsAndRestoresTaskDefinitionOnly` (Review Focus 5); `TestRestoreFailsCheckWhenPreviousTaskDefinitionIsGone`; `TestRestoreCanItselfBeRestored`; `TestRerunSelectionCoversFailedSkippedAndCancelled`; `TestRestoreAndRerunEndpointsPermissionsAndAudit`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: restore a service to its previous state and re-run failed services`.

---

### Task 5: Wiring

**Files:**
- Modify: `cmd/runner/main.go` (register the real `copy-config` and `ecs-deploy`; fakes register only under the existing guard and then replace the real ones), `internal/config/config.go` (`YUMDRAKE_ROLLOUT_POLL` default `5s`, `YUMDRAKE_ROLLOUT_TIMEOUT` default `15m`), `README.md`
- Test: `cmd/runner/main_integration_test.go`

- [ ] **Step 1: Write the failing test** `TestRunnerRegistersRealStepsByDefault` (start the runner without the fake flag and assert, through a small exported accessor on the runner, which kinds are registered and that none is a fake).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint build test test-integration`. Expected: pass.
- [ ] **Step 5: Commit** `feat: run the real deployment steps in the runner`.

---

### Task 6: End-to-end check against Floci

**Files:**
- Create: `test/e2e/deploy_floci_test.go`
- Modify: `scripts/smoke.sh` (one real deployment against Floci in the smoke stack), CI

Each test uses Plan 5's seeding (dev and prod accounts, three services running version `1.4.1`, images for `1.4.2`, config sources) and starts one orchestrator and two runners in-process with no fake steps.

- [ ] **Step 1: Write the failing tests**:
  - `TestDeployEndToEnd`: plan and start a run to `1.4.2` for three services. Final state `succeeded`. Each service's task definition is a new revision with the `1.4.2` digest and the tags; each config destination holds the manifest's bytes; the log has every AWS call with its outcome; locks are gone.
  - `TestRunnerKilledAfterRegisterAdoptsRevision`: the proxy drops the `RegisterTaskDefinition` response and the test stops that runner abruptly; the other runner finishes; exactly one new revision exists for that family. (Review Focus 1)
  - `TestCrashingVersionFailsThatServiceOnly`: one service's `1.4.2` image exits 3; the run is `partially_succeeded`; the summary gives that service, `tasks keep stopping` and exit code 3; the other two are on `1.4.2`. (Review Focus 2)
  - `TestConfigWriteDeniedHalfway`: the proxy fails the second `PutObject` with `AccessDenied`; that service's config leg fails listing written and not written; its rollout is skipped; re-run failed completes it and writes only the missing file. (Review Focus 4)
  - `TestRestoreEndToEnd`: after a successful deploy to `1.4.2`, restore; the plan shows `1.4.2` to `1.4.1` and the config changes; after starting, each service uses the exact previous task definition ARN and each destination holds its previous bytes.
  - `TestCancelDuringRollout`: cancel while services roll; legs not started are cancelled, rolling ones finish and report truthfully; final state `cancelled`; the run page data lists what changed. 
  - `TestSecondRunOnSameServiceWaits`: a real second run stays queued until the first is final.
- [ ] **Step 2: Run** `make test-e2e`; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration test-floci test-e2e smoke floci-clean`. Expected: all pass and no `floci-aws-*` container remains.
- [ ] **Step 5: Commit** `test: prove deployment, recovery and restore end to end against Floci`.
- [ ] **Step 6: Push, open a pull request against the previous plan's branch, confirm CI passes.** Do not merge it.

---

## Parallel execution

Tasks 1 and 2 together. Then Task 3 (needs 1) and Task 4 together. Then 5, then 6.

## Definition of done

- `make lint test test-integration test-floci test-e2e smoke` passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- No new third-party module.
- A reviewer confirms the five Review Focus conditions from the named tests.
