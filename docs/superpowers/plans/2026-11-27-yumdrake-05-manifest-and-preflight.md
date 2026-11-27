# Yumdrake Plan 5: Manifest and Preflight Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Yumdrake connects to AWS accounts with separate read-only and deployment credentials, offers real image versions from ECR, and turns a selection into a pinned manifest with a pre-run check and a diff that a person sees before starting. A run is revalidated against that manifest once it holds its locks. All of it tested against Floci.

**Architecture:** A package `internal/awsx` builds AWS clients for an account and a purpose (discovery for the orchestrator, deployment for the runner). The runner's `resolve` executor reads ECS, ECR, GitHub and S3, stores copies of every config file in an encrypted artifact bucket, and returns the manifest; the `revalidate` executor compares the targets with it again under the locks. The orchestrator never reads config contents.

**Tech Stack:** Go 1.26; AWS SDK for Go v2 (`config`, `credentials`, `sts`, `ecr`, `ecs`, `s3`, `kms`); Floci 2.5.0 as the local AWS.

**Spec:** `docs/superpowers/specs/2026-11-27-yumdrake-design.md` (sections 4 D4 and D8, 7.1 to 7.5, 8.1 branch rules, 9.4, 11, 16). Floci findings: `docs/research/2026-11-27-floci.md`. Read both.

**Builds on:** Plans 1 to 4. Use the actual names in the code where they differ.

No full code, by the owner's rule.

## Global Constraints

- All earlier plans' Global Constraints still apply.
- New third-party modules allowed: only `github.com/aws/aws-sdk-go-v2` and its sub-modules `config`, `credentials`, `service/sts`, `service/ecr`, `service/ecs`, `service/s3`, `service/kms`, plus what they require. Nothing else.
- The orchestrator binary must not be able to obtain deployment credentials. This is enforced by package structure and checked by a test (Task 3), not by convention.
- The orchestrator never reads the contents of an env file or a TOML. Only the runner does.
- Config contents and secrets never appear in a log line, an API response, an audit entry or an error. The diff shows env file key names, never values, and for TOMLs only "changed" or not.
- Checksums are SHA-256 computed by Yumdrake over the bytes it read or wrote. Never rely on a checksum reported by S3.
- Images are identified by digest from the moment of resolving. ECR image details are grouped by digest in our code, because one digest may come back as several entries.
- Env file branch rule: branch is the version with a leading `v` (added if absent). If the branch does not exist: in an environment labelled `dev` use branch `dev` and record that it was a fallback; in `preprod` and `prod` it is a failed check. A file missing from the chosen branch is a failed check.
- Floci listens on host port 4577 in the development stack (another tool on developers' machines owns 4566). Tests that need Floci are behind the build tag `floci` and read `YUMDRAKE_TEST_AWS_ENDPOINT`.
- Tests clean up every `floci-aws-*` container they caused and never touch containers they did not start.
- Failure behaviour that Floci cannot produce (see the findings) is tested with stub clients behind the same interfaces.

## What exists now, and how this plan is being run

Use the real names in the code, not the ones this plan's text predicted. Earlier ledgers and manager reports are under `.superpowers/sdd/` in the main checkout.

- `main` has Plans 1, 2, 3, the web foundation and a sign-in hardening change. Migrations 00001 to 00005 exist. **Plan 4 (the run engine) is being built in parallel on another branch and uses migration 00006. This plan's migration is 00007.** Do not hard-code the latest migration number in tests.
- **Tasks 1 to 6 of this plan do not need Plan 4 and are executed now.** Tasks 7 to 10 need Plan 4's `runner.Executor`, `runner.Env` and engine, and wait until Plan 4 is merged into `main` and `main` is merged into this branch.
- API rules: routes are registered in `server.routes()` and must be added to the expected map in `TestEveryRouteEnforcesItsAction` and, for list GETs, to `TestNoRouteReturnsNullForAList`. Every JSON list is `[]`, never `null`. Audited writes go through `auditedWrite`. Request bodies reject unknown fields and control characters.
- The web application already has an AWS accounts screen built against this shape, which Task 2's routes must match exactly: `GET /api/v1/admin/aws-accounts` returns `{accounts:[{id, name, account_id, discovery, deploy, environments[], created_at}]}` where each credential view is `{method: "role"|"key", role_arn, external_id, access_key_id_last4}`; `POST` takes `{name, account_id, discovery, deploy}` with each credential `{method, role_arn?, external_id?, access_key_id?, secret_access_key?}`; `POST /{id}/test` returns `{ok, error_code, message}`. See `web/src/api/types.ts` and `web/src/test/handlers.ts`. If the Go side must differ, change the web types, mocks and tests in the same task.
- Valkey keys must use `Deps.KeyPrefix`. Secrets go through `secrets.Cipher` with a purpose. `config.Scrub` and `config.Secret` keep secrets out of text.
- `make build` needs `make web-install` once (the orchestrator embeds the web app).
- Only global admins may change admin power; sign-in hardening rules are in spec section 9.

### Task 0: two small fixes left by the sign-in hardening review

- [ ] An admin can mint a token for a disabled service account (`internal/api/tokens.go`, the admin token route); it would start working when the account is re-enabled. Refuse with 422 `invalid` when the user is disabled. Test seen failing first.
- [ ] A `manage_access` non-admin can remove an identity-provider mapping or a membership that points at a group holding more than their own grants, weakening other people. Apply the same "only what you hold" rule to removals in `putIDPMapping`, `deleteIDPMapping` and `updateUser` as is applied to additions. Tests seen failing first.
- [ ] Run the suite and commit `fix: close two gaps left by the sign-in hardening review`.

## Review Focus

1. The tag is moved to another image, the branch gets a new commit, or the S3 source object changes after the plan was made: the run still uses exactly what the plan recorded. (Tasks 7, 10)
2. Someone deploys the service by hand, or another tool changes a config destination, between planning and starting: revalidation ends the run as `invalidated` before anything is touched. (Tasks 8, 10)
3. A version branch is missing in `prod`: the plan fails with a check naming the branch, and nothing falls back to `dev`. In `dev` the fallback happens and is shown. (Tasks 7, 10)
4. An AWS call is denied or throttled while resolving: the check fails with the AWS error code and request id in the log, with no credential or config content in it, and nothing was changed. (Tasks 3, 7)
5. An admin stores an access key: it is never returned, never logged, and the orchestrator cannot decrypt the deployment one. (Tasks 2, 3)

## File Structure

| Path | Responsibility |
|---|---|
| `compose.yaml`, `Makefile` | Floci in the development stack; `test-floci` |
| `test/awstest/*.go` | Seed Floci: accounts, roles, ECR images, ECS services, S3 objects |
| `internal/awsx/parse.go` | Parse ECS service ARNs, ECR URIs, S3 URIs |
| `internal/awsx/clients.go` | Build SDK clients for an account, region and endpoint |
| `internal/awsx/discovery/creds.go` | Read-only credentials, for the orchestrator |
| `internal/awsx/deploy/creds.go` | Deployment credentials, for the runner only |
| `internal/db/migrations/00007_aws.sql`, `internal/store/aws.go` | Connected AWS accounts |
| `internal/secrets/kms.go` | KMS-backed cipher |
| `internal/versions/versions.go` | Version lists from ECR, with the per-service rule and a cache |
| `internal/artifacts/artifacts.go` | Encrypted copies of config files in S3 |
| `internal/logs/s3archive.go` | S3 implementation of the log archive |
| `internal/manifest/manifest.go`, `diff.go` | Manifest types, identity hash, env key diff |
| `internal/steps/resolve/resolve.go` | The `resolve` executor |
| `internal/steps/revalidate/revalidate.go` | The `revalidate` executor |
| `internal/api/aws.go`, `versions.go`, `plan.go` | Admin AWS accounts, version lists, the plan view |
| `test/e2e/plan_floci_test.go` | Planning end to end against Floci |

---

### Task 1: Floci in the stack, seeding helpers, parsing

**Files:**
- Create: `test/awstest/floci.go`, `test/awstest/seed.go`, `test/awstest/image/Dockerfile`, `internal/awsx/parse.go`, `internal/awsx/clients.go`
- Modify: `compose.yaml` (service `floci`, image `floci/floci:2.5.0`, published on `127.0.0.1:4577`, Docker socket mounted, the image's own health check), `Makefile` (`dev-up` starts it; `test-floci` runs `go test -tags "integration floci" ./...` with `YUMDRAKE_TEST_AWS_ENDPOINT=http://localhost:4577`; `floci-clean` removes `floci-aws-*` containers), `go.mod`, `.github/workflows/ci.yml` (a `floci` job), `README.md`
- Test: `internal/awsx/parse_test.go`, `test/awstest/seed_floci_test.go`

**Interfaces:**
- Produces:

```go
// internal/awsx
type ECSServiceRef struct{ Region, Account, Cluster, Service string }
type ECRRepoRef struct{ Account, Region, Name string }
type S3Ref struct{ Bucket, Key string }
func ParseECSService(arn string) (ECSServiceRef, error)
func ParseECRRepository(uri string) (ECRRepoRef, error)
func ParseS3(uri string) (S3Ref, error)
type Options struct{ EndpointURL string } // empty for real AWS; set from YUMDRAKE_AWS_ENDPOINT_URL
func Config(ctx context.Context, region string, creds aws.CredentialsProvider, o Options) (aws.Config, error)

// test/awstest (test-only package)
type World struct{ Endpoint string /* ... */ }
func New(t *testing.T) *World                                     // fails the test if the endpoint variable is unset; registers cleanup
func (w *World) Account(id string) *Account                       // id is 12 digits; uses it as the access key id
func (a *Account) Role(name string) (arn string)                  // creates an IAM role
func (a *Account) Bucket(name string, versioned bool)
func (a *Account) PutObject(bucket, key string, body []byte)
func (a *Account) Object(bucket, key string) ([]byte, bool)
func (a *Account) Repo(region, name string, tags ...string) (uri string, digest string) // builds test/awstest/image once, pushes it under every tag
func (a *Account) RepoImage(region, name, tag, marker string) (digest string)           // a different image content under one tag, for "tag moved" tests
func (a *Account) Service(region, cluster, name, image string) (serviceARN, taskDefARN string) // cluster, task definition, service; waits until running

// A reverse proxy in front of Floci, so tests can observe and bend AWS responses.
type Proxy struct{ URL string /* ... */ }
func (w *World) Proxy(t *testing.T) *Proxy
func (p *Proxy) Requests() []Request                                   // Request: Service, Operation (from X-Amz-Target or the REST path), Account, Body
func (p *Proxy) FailNext(service, operation, awsErrorCode string, status int) // the next matching call gets this AWS error instead of reaching Floci
func (p *Proxy) Rewrite(service, operation string, fn func(body []byte) []byte) // edit matching responses on the way back
func (p *Proxy) DropAfterForward(service, operation string)            // the next matching call reaches Floci, then the connection is cut before the response
```

Behaviour:
- New configuration, both services: `YUMDRAKE_AWS_ENDPOINT_URL` (optional; an `http` or `https` URL).
- The test image is a tiny `busybox`-based image pinned by digest that sleeps; `marker` is written into a file so different markers give different digests.
- ECS services are created in the pre-created default subnet found with `ec2 describe-subnets` (see the findings).

- [ ] **Step 1: Write the failing tests**: `TestParseECSService`, `TestParseECRRepository`, `TestParseS3` tables with valid forms, and malformed inputs returning errors that name the value; `TestProxyRecordsFailsRewritesAndDrops` (tag `floci`), one sub-test per proxy method; `TestSeedCreatesReadableWorld` (tag `floci`): one account, a repo with two tags sharing a digest, a service that reaches running, a versioned bucket with an object; read each back through the SDK.
- [ ] **Step 2: Run** `make test test-floci`; confirm failure for missing packages.
- [ ] **Step 3: Add** the SDK modules, the Compose service and Make targets, and implement.
- [ ] **Step 4: Run** `make lint test test-integration test-floci`. Expected: pass; `make floci-clean` leaves no `floci-aws-*` container.
- [ ] **Step 5: Commit** `feat: add Floci to the stack with AWS parsing and seeding helpers`.

---

### Task 2: Connected AWS accounts and the KMS cipher

**Files:**
- Create: `internal/db/migrations/00007_aws.sql`, `internal/store/aws.go`, `internal/secrets/kms.go`, `internal/api/aws.go`
- Modify: `internal/config/config.go` (`YUMDRAKE_SECRETS_MODE` may now be `kms`; `YUMDRAKE_KMS_KEY_ID` required in that mode), `cmd/*/main.go`
- Test: `internal/store/aws_integration_test.go`, `internal/secrets/kms_floci_test.go`, `internal/api/aws_integration_test.go`

**Interfaces:**
- Produces:

```go
type AWSCredential struct{ Method, RoleARN, ExternalID string; AccessKeyID string } // Method: "role" | "key"; AccessKeyID shown only by its last 4 characters in the API
type AWSAccount struct{ ID int64; Name, AccountID string; Discovery, Deploy AWSCredential; CreatedAt time.Time }
func CreateAWSAccount(ctx, db, a AWSAccount, discoverySecret, deploySecret []byte) (AWSAccount, error) // ErrDuplicate on name or account id
func ListAWSAccounts(ctx, db) ([]AWSAccount, error)
func AWSAccountByAccountID(ctx, db, accountID string) (AWSAccount, error)
func DiscoverySecret(ctx, db, accountID string) ([]byte, error); func DeploySecret(ctx, db, accountID string) ([]byte, error) // ciphertext of the secret access key; nil for role method

func secrets.NewKMS(client *kms.Client, keyID string) secrets.Cipher // purpose is passed as the encryption context {"purpose": purpose}
```

Routes (`manage_connections`, audited):

| Method and path | Does |
|---|---|
| `GET /api/v1/admin/aws-accounts` | List, with which environments use each. No secrets |
| `POST /api/v1/admin/aws-accounts` | Name, account id, and for each of discovery and deploy: a role ARN with optional external id, or an access key id and secret. Secrets are encrypted with purposes `aws-discovery` and `aws-deploy` |
| `POST /api/v1/admin/aws-accounts/{id}/test` | Uses the discovery credential: `GetCallerIdentity` and checks the account id matches. Reports ok or the AWS error code |

- `Engine.Create` (Plan 4) now refuses with `ErrNotRunnable` and a clear message when the environment's `aws_account`, or the account of any selected service's ECR repository, is not connected.

- [ ] **Step 1: Write the failing tests**: `TestAWSAccountRoundTripStoresOnlyCiphertext`; `TestDuplicateAccountIDIsErrDuplicate`; `TestKMSCipherRoundTripAndWrongPurposeFails` (Floci); `TestCreateAccountNeverReturnsSecretAndAudits` (search every response and the audit detail for the secret); `TestTestConnectionReportsAccountMismatch`; `TestCreateRunRefusedWhenAccountNotConnected`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration test-floci`. Expected: pass.
- [ ] **Step 5: Commit** `feat: connect AWS accounts with separate discovery and deployment credentials`.

---

### Task 3: Credentials for the two services

**Files:**
- Create: `internal/awsx/discovery/creds.go`, `internal/awsx/deploy/creds.go`, `internal/awsx/boundary_test.go`
- Test: `internal/awsx/discovery/creds_floci_test.go`, `internal/awsx/deploy/creds_floci_test.go`

**Interfaces:**
- Produces, the same shape in both packages:

```go
type Provider struct{ /* pool, cipher, base config, options, cache */ }
func New(pool *pgxpool.Pool, c secrets.Cipher, base aws.Config, o awsx.Options) *Provider
func (p *Provider) For(ctx context.Context, accountID, region, sessionName string) (aws.Config, error)
var ErrNotConnected error
```

Behaviour:
- Role method: `sts:AssumeRole` from the service's own identity with the stored external id and the given session name (`yumdrake-<service>-run-<id>` for runs). Key method: static credentials from the decrypted secret.
- Credentials are cached per account until 5 minutes before they expire.
- Errors name the account and the AWS error code, never a key or secret.
- `discovery` decrypts with purpose `aws-discovery`, `deploy` with `aws-deploy`.
- Boundary: `cmd/orchestrator` and everything it imports must not import `internal/awsx/deploy`. `boundary_test.go` runs `go list -deps ./cmd/orchestrator` and fails if that package appears.

- [ ] **Step 1: Write the failing tests**: `TestAssumeRoleYieldsTargetAccountIdentity` (both packages, Floci); `TestKeyMethodUsesStoredKey`; `TestUnknownAccountIsErrNotConnected`; `TestCredentialsAreCachedUntilNearExpiry` (count STS calls through a counting HTTP client); `TestErrorNamesCodeAndNeverTheSecret` (role that does not exist); `TestOrchestratorCannotImportDeployCredentials`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.** Wire `discovery.Provider` into the orchestrator and `deploy.Provider` into the runner.
- [ ] **Step 4: Run** `make lint build test test-integration test-floci`. Expected: pass.
- [ ] **Step 5: Commit** `feat: build AWS credentials per account with a hard boundary between services`.

---

### Task 4: Version lists

**Files:**
- Create: `internal/versions/versions.go`, `internal/versions/semver.go`, `internal/api/versions.go`
- Test: `internal/versions/versions_test.go`, `internal/versions/versions_floci_test.go`, `internal/api/versions_integration_test.go`

**Interfaces:**
- Produces:

```go
type Version struct{ Tag, Digest string; PushedAt time.Time }
type ECR interface{ DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, o ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) }
func List(ctx context.Context, client ECR, repo awsx.ECRRepoRef, rule spec.VersionRule) ([]Version, error)
func Common(perService map[string][]Version) []string // tags present for every service, in the order of the first service's list
func CompareSemver(a, b string) int                    // optional leading "v"; a pre-release sorts before its release
type Service struct{ /* discovery provider, cache, ttl */ }
func (s *Service) ForJob(ctx context.Context, job spec.Job, inv spec.Inventory, envName string) (JobVersions, error)
type JobVersions struct{ Groups map[string]GroupVersions }
type GroupVersions struct{ Common []string; Services map[string][]Version; Unavailable map[string]string } // service -> reason (not in this environment, or the AWS error code)
```

Behaviour:
- `List` pages through `DescribeImages`, groups entries by digest, keeps tags matching the rule's pattern, sorts by `semver` (newest first, ties by push time) or `pushed`, and cuts at the rule's limit. Untagged images are ignored.
- `ForJob` lists each service's repository for that environment through the discovery credentials, at most 8 at a time, caching each repository's list in Valkey for 60 seconds. If Valkey is down it lists without caching. One service's failure appears in `Unavailable` and does not fail the others.
- Route `GET /api/v1/jobs/{id}/versions?environment=<name>` (`view`) returns `JobVersions`.

- [ ] **Step 1: Write the failing tests**: `TestCompareSemver` table (`1.4.2` vs `1.4.10`; `v2.0.0` vs `2.0.0` equal; `1.5.0-rc1` before `1.5.0`; `rc2` after `rc1`; non-versions sort last and stable); `TestListGroupsByDigestFiltersSortsAndLimits` with a stub returning two entries for one digest, a `latest` tag, a commit-hash tag and three pages; `TestListSortByPushed`; `TestCommon`; `TestListAgainstFloci` (repo seeded with `1.4.1`, `1.4.2`, `latest`); `TestForJobIsolatesOneServicesFailure`; `TestForJobCachesForSixtySecondsAndWorksWithoutValkey`; `TestVersionsEndpoint`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration test-floci`. Expected: pass.
- [ ] **Step 5: Commit** `feat: list image versions from ECR by each service's rule`.

---

### Task 5: Artifact store and S3 log archive

**Files:**
- Create: `internal/artifacts/artifacts.go`, `internal/logs/s3archive.go`
- Modify: `internal/config/config.go` (`YUMDRAKE_ARTIFACT_BUCKET` required for the runner; `YUMDRAKE_ARTIFACT_KMS_KEY_ID` optional; `YUMDRAKE_LOG_BUCKET` optional for the orchestrator, replacing the directory archive when set)
- Test: `internal/artifacts/artifacts_floci_test.go`, `internal/logs/s3archive_floci_test.go`

**Interfaces:**
- Produces:

```go
type Store struct{ /* s3 client, bucket, kms key id */ }
func New(client *s3.Client, bucket, kmsKeyID string) *Store
func (s *Store) Put(ctx context.Context, runID int64, service, kind, name string, previous bool, data []byte) (key, sha256hex string, err error)
func (s *Store) Get(ctx context.Context, key, wantSHA256 string) ([]byte, error) // ErrCorrupt when the bytes do not hash to wantSHA256
func logs.NewS3Archive(client *s3.Client, bucket string) logs.Archive
```

Behaviour:
- Keys: `manifests/<run>/<service>/<kind>/<name>` and the same under `.../previous/` for the prior destination content. Objects are written with server-side encryption (KMS with the key id when given, otherwise S3-managed).
- `Get` always verifies the checksum.
- These use Yumdrake's own account and the runner's own identity, not a connected account.

- [ ] **Step 1: Write the failing tests**: `TestPutGetRoundTripAndChecksum`; `TestGetDetectsCorruption` (overwrite the object behind its back); `TestPreviousCopiesUseSeparateKeys`; `TestS3ArchivePutGet`; `TestArchiverWorksWithS3Archive` (Plan 4's archiver test run against it).
- [ ] **Step 2: Run** `make test-floci`; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration test-floci`. Expected: pass.
- [ ] **Step 5: Commit** `feat: store config copies and archived logs in S3`.

---

### Task 6: Manifest model and diff

**Files:**
- Create: `internal/manifest/manifest.go`, `internal/manifest/diff.go`
- Test: `internal/manifest/manifest_test.go`, `internal/manifest/diff_test.go`

**Interfaces:**
- Produces:

```go
type Manifest struct {
    ID                         string // sha256 hex over the pinned inputs; see Identity
    RunID                      int64
    Environment, Label, AWSAccount string
    InventoryCommit, JobCommit string
    Services                   []Service // sorted by name
    Checks                     []Check
}
type Service struct {
    Name, Group, Version       string
    ImageDigest                string
    ECSService, ECRRepository  string
    PreviousTaskDefinition     string // full ARN with revision
    PreviousVersion            string // read from the previous revision's tags or image; "" if unknown
    Config                     []ConfigItem // env files then TOMLs, in inventory order
}
type ConfigItem struct {
    Kind, Name                 string // Kind: "env" | "toml"
    Source, Destination        string
    Branch, Commit             string // env only
    Fallback                   bool   // env only: branch dev was used because the version branch is missing
    SourceVersionID            string // toml only, when the bucket has versioning
    SHA256, ArtifactKey        string
    Size                       int64
    PreviousExists             bool
    PreviousSHA256, PreviousArtifactKey string
    Change                     string // "new" | "changed" | "unchanged"
    Keys                       *KeyDiff // env only, when changed
}
type KeyDiff struct{ Added, Removed, Changed []string } // names only, sorted
type Check struct{ Name, Service, Status, Detail string } // Status: "passed" | "failed" | "not_verified"

func Identity(m Manifest) string
func (m Manifest) Failed() []Check
func (m Manifest) Redacted() Manifest               // drops ArtifactKey and PreviousArtifactKey; this is what the API serves
func EnvKeyDiff(previous, next []byte) KeyDiff
```

Behaviour:
- `Identity` hashes a canonical JSON of: environment, account, both commits, and per service the name, version, digest, ECS ARN, previous task definition, and per config item the kind, name, destination and SHA-256. It does not include checks, diff fields, artifact keys or the run id, so two plans of the same inputs have the same identity.
- `EnvKeyDiff` reads dotenv lines (`KEY=value`, optional `export `, `#` comments and blank lines ignored, surrounding quotes not interpreted). It returns names whose value was added, removed or changed. It never returns or logs a value. A line it cannot read as a key is ignored.

- [ ] **Step 1: Write the failing tests**: `TestIdentityStableAcrossOrderingAndRuns`; `TestIdentityChangesWithDigestCommitOrChecksum` table; `TestIdentityIgnoresChecksAndArtifactKeys`; `TestRedactedDropsArtifactKeys`; `TestEnvKeyDiff` table (added, removed, changed, unchanged, comments, `export`, duplicate keys last wins, CRLF line ends, a value containing `=`); `TestEnvKeyDiffNeverContainsValues` (search the JSON of the result for every value in the fixtures).
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement** as pure code.
- [ ] **Step 4: Run** `make lint test`. Expected: pass.
- [ ] **Step 5: Commit** `feat: add the deployment manifest, its identity and the env key diff`.

---

### Task 7: The resolve executor

**Files:**
- Create: `internal/steps/resolve/resolve.go`, `internal/steps/resolve/branch.go`
- Test: `internal/steps/resolve/resolve_test.go`, `internal/steps/resolve/resolve_floci_test.go`

**Interfaces:**
- Consumes: `runner.Executor` and `runner.Env`, `deploy.Provider`, `artifacts.Store`, a GitHub source lookup, `manifest`.
- Produces:

```go
type Deps struct {
    AWS       func(ctx context.Context, accountID, region, session string) (aws.Config, error)
    GitHub    func(ctx context.Context, owner string) (Source, error) // Source: BranchExists, HeadCommit, ReadFile (Plan 2's client)
    Artifacts *artifacts.Store
    Parallel  int // default 8
}
func New(d Deps) runner.Executor
func BranchFor(version string) string                                  // "v" + version unless it already starts with "v"
func ChooseBranch(versionBranchExists bool, label string) (branch string, fallback bool, ok bool)
```

Behaviour, per selected service (services in parallel up to `Parallel`):
1. `ecs:DescribeServices` then `ecs:DescribeTaskDefinition` with tags: record the previous task definition ARN and previous version. A service that does not exist, or is not `ACTIVE`, is a failed check.
2. `ecr:DescribeImages` for the tag: record the digest. A missing tag is a failed check.
3. Each env file: decide the branch with the Global Constraints rule, read the branch's head commit, read the file at that commit, hash it, store the copy.
4. Each TOML: `s3:GetObject` (recording the version id if any), hash, store the copy.
5. Each destination: `s3:GetObject`; absent is recorded as not existing; otherwise hash and store the previous copy. Set `Change`, and `Keys` for a changed env file.
6. Checks recorded per service: `service exists`, `image exists`, one per config source, `destination readable`, and `write permission` as `not_verified` (it cannot be proven without writing).

Outcome:
- The leg's result is the manifest JSON with its identity, whether or not checks failed, so the person can see which. Any failed check makes the leg `failed` with short reason `preflight failed: <n> checks`.
- It makes no change to any environment. It writes only to the artifact bucket.
- Every AWS and GitHub call is logged with its outcome; errors carry the AWS error code and request id. File contents are never logged; a file is logged by name, size and the first 12 characters of its checksum.
- Recovery: running it again produces the same identity for unchanged inputs. It needs no `Env.Do`.

- [ ] **Step 1: Write the failing unit tests** with stub ECS, ECR, S3 and GitHub behind small interfaces: `TestBranchFor`; `TestChooseBranch` table over exists/missing and the three labels; `TestResolveHappyPathBuildsManifest`; `TestMissingTagIsFailedCheckAndLegFails`; `TestMissingBranchInProdFailsAndInDevFallsBack` (Review Focus 3); `TestMissingFileInChosenBranchFails`; `TestAccessDeniedIsFailedCheckWithCodeAndRequestID` and `TestThrottlingIsRetriedThenFails` (Review Focus 4); `TestNoConfigServiceResolves`; `TestLogNeverContainsFileContents`; `TestSameInputsGiveSameIdentity`.
- [ ] **Step 2: Write the failing Floci test** `TestResolveAgainstFloci`: two services seeded with images, running services, an env file served by the fake GitHub and a TOML in a versioned bucket; the manifest has the real digest, the real previous task definition ARN, the right checksums, and the artifact objects exist and hash correctly.
- [ ] **Step 3: Run**; confirm failure.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** `make lint test test-integration test-floci`. Expected: pass.
- [ ] **Step 6: Commit** `feat: resolve a run into a pinned manifest with preflight checks`.

---

### Task 8: The revalidate executor

**Files:**
- Create: `internal/steps/revalidate/revalidate.go`
- Test: `internal/steps/revalidate/revalidate_test.go`, `internal/steps/revalidate/revalidate_floci_test.go`

**Interfaces:**
- Produces: `func New(d resolve.Deps) runner.Executor`

Behaviour, with the run's locks already held:
- Each service still runs the manifest's `PreviousTaskDefinition`. If not: failed, short reason `changed since planning`, detail naming the service and both revisions.
- Each image digest still exists in its repository.
- Each config destination's current content still hashes to the manifest's `PreviousSHA256` (or is still absent).
- Each stored artifact still hashes to its recorded checksum.
- Any mismatch fails the leg, which Plan 4 turns into `invalidated`. It changes nothing.

- [ ] **Step 1: Write the failing tests**: `TestRevalidatePassesWhenNothingChanged`; `TestServiceRedeployedSincePlanningFails`; `TestDestinationChangedSincePlanningFails`; `TestDestinationCreatedSincePlanningFails`; `TestImageDeletedSincePlanningFails`; `TestCorruptArtifactFails`; and on Floci `TestRevalidateDetectsHandDeploy` (register a new revision and update the service by hand after resolving). (Review Focus 2)
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-floci`. Expected: pass.
- [ ] **Step 5: Commit** `feat: revalidate targets against the manifest under the locks`.

---

### Task 9: Wiring and the plan view

**Files:**
- Create: `internal/api/plan.go`
- Modify: `cmd/runner/main.go` (register `resolve` and `revalidate`; under the fake-steps guard only `copy-config` and `ecs-deploy` stay fake), `internal/engine/advance.go` (store the manifest and identity from the resolve leg's result when it settles, failed or not), `docs/superpowers/specs/2026-11-27-yumdrake-design.md` if any behaviour here differs from it
- Test: `internal/api/plan_integration_test.go`, `internal/engine/manifest_integration_test.go`

Routes:

| Method and path | Needs | Does |
|---|---|---|
| `GET /api/v1/runs/{id}/plan` | `view` | The redacted manifest: per service the current and requested version and task definition, each config item's change and key names, whether a fallback branch was used, and the checks |

- Approval (Plan 7) will bind to `Run.ManifestID`; this plan makes sure it is set before a run can leave `resolving`.

- [ ] **Step 1: Write the failing tests**: `TestManifestStoredWhenResolveSucceeds`; `TestManifestStoredWhenPreflightFailsAndRunIsFailed`; `TestPlanEndpointServesRedactedManifest` (no artifact key, no value from any fixture env file anywhere in the body); `TestPlanEndpoint404BeforeResolved`.
- [ ] **Step 2: Run**; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint build test test-integration test-floci`. Expected: pass.
- [ ] **Step 5: Commit** `feat: store the manifest on the run and serve the plan`.

---

### Task 10: End-to-end check against Floci

**Files:**
- Create: `test/e2e/plan_floci_test.go`
- Modify: `Makefile` (`test-e2e` includes the `floci` tag when the endpoint variable is set), `scripts/smoke.sh`, CI

Each test: seed Floci (a dev account and a prod account, two environments, three services with images and running ECS services, env files on the fake GitHub with a `v1.4.2` branch and a `dev` branch, a TOML bucket), start one orchestrator and one runner in-process with real `resolve` and `revalidate` and fake `copy-config` and `ecs-deploy`, connect the accounts through the API, sign in.

- [ ] **Step 1: Write the failing tests**:
  - `TestPlanShowsRealStateAndDiff`: version list offers the seeded tags; a planned run's plan shows each service's current and requested version, a changed env file with the right key names, an unchanged TOML, all checks passed except `write permission` as `not_verified`.
  - `TestPinnedInputsSurviveLaterChanges`: after planning, move the tag to a different image, push a commit to the branch, overwrite the TOML source; start the run; the fake deploy step receives the manifest's original digest and artifact checksums. (Review Focus 1)
  - `TestHandDeployInvalidatesRun`: change the service by hand after planning; start; the run ends `invalidated`, no step leg ran, locks released. (Review Focus 2)
  - `TestProdWithoutVersionBranchFailsPreflight` and `TestDevFallsBackToDevBranchAndSaysSo`. (Review Focus 3)
  - `TestOrchestratorNeverSeesConfigContents`: capture every HTTP request the orchestrator process makes to the Floci endpoint through a recording proxy; none is an S3 `GetObject` on a config bucket or the artifact bucket.
- [ ] **Step 2: Run** `make test-e2e`; confirm failure.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make lint test test-integration test-floci test-e2e smoke floci-clean`. Expected: all pass and no `floci-aws-*` container remains.
- [ ] **Step 5: Commit** `test: prove planning end to end against Floci`.
- [ ] **Step 6: Push, open a pull request against the previous plan's branch, confirm CI passes.** Do not merge it.

---

## Parallel execution

Task 1 first. Then Tasks 2, 4's pure parts (`semver`, grouping) and 6 together. Then Tasks 3 and 5 together. Then Task 4's service and endpoint, 7 and 8 together (8 needs `resolve.Deps` from 7: agree the type first, then run both). Then 9, then 10.

## Definition of done

- `make lint test test-integration test-floci test-e2e smoke` passes locally and in CI.
- Every named test exists, was seen failing first, and passes.
- Only AWS SDK modules were added.
- A reviewer confirms the five Review Focus conditions from the named tests.
