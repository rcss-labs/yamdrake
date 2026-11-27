// Request and response shapes for every route of the API contract.
// Field names follow the Go structs in internal/api and internal/spec (they win over plan text);
// where the plans give none (access administration, tokens, CLI sign-in, SSO, AWS accounts)
// the names are snake_case in the same style. Timestamps are ISO 8601 strings.

// ---- Access ---------------------------------------------------------------

export type Action =
  | 'view'
  | 'view_logs'
  | 'run'
  | 'cancel'
  | 'approve'
  | 'restore'
  | 'manage_jobs'
  | 'manage_connections'
  | 'manage_access'
  | 'view_audit'

export const ALL_ACTIONS: readonly Action[] = [
  'view',
  'view_logs',
  'run',
  'cancel',
  'approve',
  'restore',
  'manage_jobs',
  'manage_connections',
  'manage_access',
  'view_audit',
]

/** A grant as the server sends it: an empty label or environment, or folder_id 0, means "any". */
export interface Grant {
  action: Action
  label: string
  environment: string
  folder_id: number
}

/** GET /api/v1/me and the body of a successful POST /api/v1/auth/login. */
export interface Me {
  id: number
  email: string
  name: string
  admin: boolean
  grants: Grant[]
}

/** GET /api/v1/auth/methods */
export interface AuthMethods {
  password: boolean
  saml: boolean
}

/** POST /api/v1/auth/login */
export interface LoginRequest {
  email: string
  password: string
}

// ---- Problems and definitions (internal/spec) -------------------------------
// Slices in spec structs are built by appending to nil, so the server sends null when empty:
// every list of a parsed definition or service below is `T[] | null`. Lists in the API's own
// response structs (inventory, jobs, folders, problems) are never null.

export interface Problem {
  file: string
  /** 0 when the problem is about the whole file. */
  line: number
  message: string
}

export interface EnvironmentSummary {
  name: string
  label: string
  aws_account: string
}

export interface ServiceSummary {
  name: string
  /** Names of the environments that have a block for this service. */
  environments: string[]
}

/** The rejected newer commit of the inventory repository; the active commit stays in use. */
export interface RejectedInventory {
  commit: string
  problems: Problem[]
}

/** GET /api/v1/inventory, POST /api/v1/inventory/sync and PUT /api/v1/admin/inventory-source. */
export interface Inventory {
  /** Empty before the first activation. */
  commit: string
  activated_at: string | null
  checked_at: string | null
  sync_error: string
  sync_error_at: string | null
  rejected: RejectedInventory | null
  environments: EnvironmentSummary[]
  services: ServiceSummary[]
}

export interface EnvFile {
  name: string
  source: string
  destination: string
  line: number
}

export interface Toml {
  name: string
  source: string
  destination: string
  line: number
}

export interface ServiceEnvironment {
  ecs_service: string
  ecr_repository: string
  /** The Go server sends null, not [], when the file has none. */
  env_files: EnvFile[] | null
  tomls: Toml[] | null
  line: number
}

export interface VersionRule {
  pattern: string
  sort: string
  limit: number
}

/** GET /api/v1/inventory/services/{name} */
export interface Service {
  file: string
  name: string
  versions: VersionRule
  environments: Record<string, ServiceEnvironment>
  line: number
}

export interface JobNeed {
  step: string
  scope: string
  line: number
}

export interface JobStep {
  name: string
  use: string
  group: string
  /** 0 means not set. */
  max_parallel: number
  /** Nanoseconds (Go time.Duration); 0 means not set. */
  timeout: number
  /** null when the step needs nothing (the Go server leaves the list nil). */
  needs: JobNeed[] | null
  line: number
}

export interface JobGroup {
  id: string
  label: string
  services: string[] | null
  line: number
}

export interface JobApproval {
  labels: string[] | null
  /** Nanoseconds (Go time.Duration). */
  expires: number
  line: number
}

/** The parsed job definition (last good version). */
export interface JobDefinition {
  file: string
  id: string
  title: string
  labels: string[] | null
  labels_line: number
  groups: JobGroup[] | null
  approval: JobApproval | null
  slack_channel: string
  steps: JobStep[] | null
}

// ---- Folders and jobs -------------------------------------------------------

export interface Folder {
  id: number
  name: string
}

export interface FoldersResponse {
  folders: Folder[]
}

export interface CreateFolderRequest {
  name: string
}

export interface JobSummary {
  id: number
  job_id: string
  title: string
  folder_id: number
  folder: string
  repo: string
  branch: string
  path: string
  /** False until a good definition has been read; the job cannot be started then. */
  runnable: boolean
  /** The last good commit (empty when there is none). */
  commit: string
  synced_at: string | null
  checked_at: string | null
  problem_count: number
  sync_error: string
  sync_error_at: string | null
  // Task 9 adds the two fields below to the Go list response; until then they are absent. Null or absent means "not known".
  /** Every service the job deploys across all groups, de-duplicated, in definition order. */
  services?: string[] | null
  /** Labels whose runs need approval (empty when none do). */
  approval_labels?: string[] | null
}

export interface JobsResponse {
  jobs: JobSummary[]
}

/** GET /api/v1/jobs/{id}, and the answer of POST /jobs, PATCH /jobs/{id} and POST /jobs/{id}/sync. */
export interface JobDetail extends JobSummary {
  /** The last good definition; null until the first good read. */
  definition: JobDefinition | null
  problems: Problem[]
  /** The commit the problems were found in. */
  problems_commit: string
}

export interface RegisterJobRequest {
  folder_id: number
  repo: string
  branch: string
  path: string
}

export interface MoveJobRequest {
  folder_id: number
}

export interface InventorySourceRequest {
  repo: string
  branch: string
  path: string
}

// ---- GitHub connections -----------------------------------------------------

export type GithubMethod = 'token' | 'app'

export interface GithubConnection {
  id: number
  owner: string
  method: GithubMethod
  app_id: number
  installation_id: number
  created_at: string
}

export interface GithubConnectionsResponse {
  connections: GithubConnection[]
}

/** Method "token" takes only token; method "app" takes app_id, installation_id and private_key. */
export interface CreateGithubConnectionRequest {
  owner: string
  method: GithubMethod
  token?: string
  app_id?: number
  installation_id?: number
  private_key?: string
}

// ---- AWS accounts (Plan 5, Task 2) -----------------------------------------------

export type AwsMethod = 'role' | 'key'

/** What the server shows of a credential: never a secret; keys only by their last 4 characters. */
export interface AwsCredentialView {
  method: AwsMethod
  role_arn: string
  external_id: string
  access_key_id_last4: string
}

export interface AwsAccount {
  id: number
  name: string
  account_id: string
  discovery: AwsCredentialView
  deploy: AwsCredentialView
  /** Names of the environments that use this account. */
  environments: string[]
  created_at: string
}

export interface AwsAccountsResponse {
  accounts: AwsAccount[]
}

/** Method "role" takes role_arn and optional external_id; method "key" takes access_key_id and secret_access_key. */
export interface AwsCredentialInput {
  method: AwsMethod
  role_arn?: string
  external_id?: string
  access_key_id?: string
  secret_access_key?: string
}

export interface CreateAwsAccountRequest {
  name: string
  account_id: string
  discovery: AwsCredentialInput
  deploy: AwsCredentialInput
}

/** POST /api/v1/admin/aws-accounts/{id}/test */
export interface AwsAccountTestResult {
  ok: boolean
  /** The AWS error code when ok is false (for example "AccessDenied"). */
  error_code: string
  message: string
}

// ---- Access administration (Plan 3, Task 6) ---------------------------------

export type UserSource = 'local' | 'saml' | 'service'

export interface GroupRef {
  id: number
  name: string
}

export interface AdminUser {
  id: number
  email: string
  name: string
  source: UserSource
  disabled: boolean
  groups: GroupRef[]
  last_sign_in_at: string | null
  created_at: string
}

export interface UsersResponse {
  users: AdminUser[]
}

export interface CreateUserRequest {
  email: string
  name: string
  source: 'local' | 'service'
  /** At least 12 characters. */
  password: string
}

/** Disable or enable, and/or replace the manual group memberships. */
export interface UpdateUserRequest {
  disabled?: boolean
  group_ids?: number[]
}

export interface AdminGroup {
  id: number
  name: string
  admin: boolean
  member_count: number
}

export interface GroupsResponse {
  groups: AdminGroup[]
}

export interface CreateGroupRequest {
  name: string
  admin?: boolean
}

export interface UpdateGroupRequest {
  admin: boolean
}

export interface IdpMapping {
  idp_group: string
  group_id: number
}

export interface IdpMappingsResponse {
  mappings: IdpMapping[]
}

export interface PutIdpMappingRequest {
  group_id: number
}

export interface AdminGrant extends Grant {
  id: number
  group_id: number
}

export interface GrantsResponse {
  grants: AdminGrant[]
}

/** Scopes left out or empty (label "", environment "", folder_id 0) match anything. */
export interface CreateGrantRequest {
  group_id: number
  action: Action
  label?: string
  environment?: string
  folder_id?: number
}

export interface AuditEntry {
  id: number
  at: string
  /** The actor's email, or "system". */
  actor: string
  /** For example "grant.create" or "auth.login". */
  action: string
  target_type: string
  target_id: string
  /** Safe values only; secrets are never recorded. */
  detail: Record<string, unknown>
}

/** GET /api/v1/admin/audit?before=&limit= (newest first, limit at most 200). */
export interface AuditResponse {
  entries: AuditEntry[]
  /** Pass as `before` for the next page; null on the last page. */
  next_before: number | null
}

// ---- Single sign-on ---------------------------------------------------------

export interface SsoSettings {
  enabled: boolean
  idp_metadata_xml: string
  entity_id: string
  groups_attribute: string
  email_attribute: string
  name_attribute: string
  /** Read-only: the addresses to give the identity provider. */
  acs_url: string
  metadata_url: string
}

export type UpdateSsoRequest = Omit<SsoSettings, 'acs_url' | 'metadata_url'>

export interface SsoTestResult {
  ok: boolean
  message: string
}

// ---- Tokens and CLI sign-in --------------------------------------------------

export interface ApiToken {
  id: number
  name: string
  created_at: string
  expires_at: string
  last_used_at: string | null
}

export interface TokensResponse {
  tokens: ApiToken[]
}

export interface CreateTokenRequest {
  name: string
  /** Lifetime in days; the server defaults to 90 and caps at 365. */
  ttl_days?: number
}

/** The plaintext is shown once, on creation. */
export interface CreatedToken extends ApiToken {
  plaintext: string
}

/** POST /api/v1/cli/login (no sign-in needed). */
export interface DeviceLoginStart {
  device_code: string
  user_code: string
  verify_url: string
  /** Seconds between polls. */
  interval: number
}

/** POST /api/v1/cli/login/confirm (signed in). */
export interface DeviceLoginConfirmRequest {
  user_code: string
}

/** POST /api/v1/cli/login/token */
export interface DeviceLoginPollRequest {
  device_code: string
}

export type DeviceLoginPollResponse = { status: 'pending' } | { status: 'confirmed'; token: string }

/** The body of every error response. */
export interface ErrorBody {
  error: { code: string; message: string }
}
