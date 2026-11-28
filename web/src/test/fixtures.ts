// The data behind the mock API. `createMockState()` returns a fresh copy; handlers.ts resets to one
// before every test. The Me presets and `knownPassword` are what tests sign in with.
import type {
  AdminGrant,
  AdminGroup,
  AdminUser,
  ApiToken,
  AuditEntry,
  ApiLogGap,
  ApiLogLine,
  AwsAccount,
  Folder,
  Freeze,
  GithubConnection,
  IdpMapping,
  Inventory,
  JobDetail,
  JobDefinition,
  Me,
  Plan,
  RunDetail,
  Service,
  SlackSettings,
  SsoSettings,
} from '../api/types'
import { logsFixture, plansFixture, runsFixtureList } from './runFixtures'

export * from './runFixtures'

/** The password every preset person signs in with on the mock server. */
export const knownPassword = 'correct-horse-battery'

// ---- People (what GET /api/v1/me returns) -----------------------------------

/** A global admin: every action everywhere, no grants needed. */
export const adminMe: Me = { id: 1, email: 'dana@example.com', name: 'Dana Levi', admin: true, grants: [] }

/** Signed in with no grants: may only view. */
export const viewerMe: Me = { id: 5, email: 'lior@example.com', name: 'Lior Amar', admin: false, grants: [] }

/** May register jobs and create folders anywhere (manage_jobs, unscoped). */
export const jobManagerMe: Me = {
  id: 6,
  email: 'yael@example.com',
  name: 'Yael Barak',
  admin: false,
  grants: [{ action: 'manage_jobs', label: '', environment: '', folder_id: 0 }],
}

/** May manage users, groups, grants, SSO (manage_access). */
export const accessManagerMe: Me = {
  id: 7,
  email: 'ron@example.com',
  name: 'Ron Shapira',
  admin: false,
  grants: [{ action: 'manage_access', label: '', environment: '', folder_id: 0 }],
}

/** May manage GitHub connections, AWS accounts and the inventory source (manage_connections). */
export const connectionsManagerMe: Me = {
  id: 8,
  email: 'tamar@example.com',
  name: 'Tamar Golan',
  admin: false,
  grants: [{ action: 'manage_connections', label: '', environment: '', folder_id: 0 }],
}

/** May read the audit log (view_audit). */
export const auditorMe: Me = {
  id: 9,
  email: 'eli@example.com',
  name: 'Eli Navon',
  admin: false,
  grants: [{ action: 'view_audit', label: '', environment: '', folder_id: 0 }],
}

/** A QA person with scoped grants: run and view logs in dev only, approve nothing. */
export const qaMe: Me = {
  id: 3,
  email: 'omer@example.com',
  name: 'Omer Katz',
  admin: false,
  grants: [
    { action: 'run', label: 'dev', environment: '', folder_id: 0 },
    { action: 'view_logs', label: 'dev', environment: '', folder_id: 0 },
  ],
}

/** Every preset, so the mock server can sign any of them in by email. */
export const mePresets: Me[] = [adminMe, viewerMe, jobManagerMe, accessManagerMe, connectionsManagerMe, auditorMe, qaMe]

// ---- Inventory -----------------------------------------------------------------

const t = (iso: string) => iso

export const inventoryFixture: Inventory = {
  commit: 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678',
  activated_at: t('2026-11-27T08:30:00Z'),
  checked_at: t('2026-11-27T09:00:00Z'),
  sync_error: '',
  sync_error_at: null,
  rejected: {
    commit: 'ffee00112233445566778899aabbccddeeff0011',
    problems: [
      { file: 'services/billing.yaml', line: 14, message: 'ecs_service is required' },
      { file: 'environments.yaml', line: 0, message: 'environment "hetzner-09" is declared twice' },
    ],
  },
  environments: [
    { name: 'hetzner-01', label: 'dev', aws_account: '111111111111' },
    { name: 'hetzner-02', label: 'dev', aws_account: '111111111111' },
    { name: 'hetzner-03', label: 'dev', aws_account: '111111111111' },
    { name: 'preprod-01', label: 'preprod', aws_account: '222222222222' },
    { name: 'prod-01', label: 'prod', aws_account: '333333333333' },
  ],
  services: [
    { name: 'backend-api', environments: ['hetzner-01', 'hetzner-02', 'hetzner-03', 'preprod-01', 'prod-01'] },
    { name: 'billing', environments: ['hetzner-01', 'hetzner-02', 'preprod-01', 'prod-01'] },
  ],
}

function serviceFixture(name: string, environments: string[]): Service {
  const perEnvironment: Service['environments'] = {}
  environments.forEach((environment, index) => {
    perEnvironment[environment] = {
      ecs_service: `${name}-${environment}`,
      ecr_repository: `${environment.startsWith('prod') ? '333333333333' : '111111111111'}.dkr.ecr.eu-central-1.amazonaws.com/${name}`,
      env_files:
        name === 'backend-api'
          ? [
              {
                name: 'backend-api.env',
                source: `example-org/env-config-repo/${environment}/backend-api/backend-api.env`,
                destination: `s3://${environment}-config/env/backend-api/backend-api.env`,
                line: 12 + index,
              },
            ]
          : null,
      tomls:
        name === 'billing'
          ? [
              {
                name: 'billing.toml',
                source: `s3://main-config-bucket/${environment}/billing.toml`,
                destination: `s3://${environment}-config/toml/billing.toml`,
                line: 20 + index,
              },
            ]
          : null,
      line: 10 + index,
    }
  })
  return {
    file: `services/${name}.yaml`,
    name,
    versions: { pattern: '^\\d+\\.\\d+\\.\\d+$', sort: 'semver', limit: 20 },
    environments: perEnvironment,
    line: 2,
    source: serviceYaml(name),
  }
}

function serviceYaml(name: string): string {
  return [
    'apiVersion: yumdrake/v1',
    'kind: Service',
    `name: ${name}`,
    'versions:',
    "  pattern: '^\\d+\\.\\d+\\.\\d+$'",
    '  sort: semver',
    '  limit: 20',
    '',
  ].join('\n')
}

export const servicesFixture: Service[] = inventoryFixture.services.map((summary) =>
  serviceFixture(summary.name, summary.environments),
)

// ---- Folders and jobs -----------------------------------------------------------

export const foldersFixture: Folder[] = [
  { id: 1, name: 'Backend' },
  { id: 2, name: 'Portal' },
  { id: 3, name: 'Releases' },
]

export function buildJobDefinition(id: string, title: string, groups: NonNullable<JobDefinition['groups']>, approvalLabels: string[]): JobDefinition {
  return {
    file: `jobs/${id}.yaml`,
    id,
    title,
    labels: ['dev', 'preprod', 'prod'],
    labels_line: 4,
    groups,
    approval: approvalLabels.length > 0 ? { labels: approvalLabels, expires: 4 * 3600 * 1e9, line: 9 } : null,
    slack_channel: '#deployments',
    steps: [
      { name: 'copy-config', use: 'copy-config', group: groups[0]?.id ?? '', max_parallel: 4, timeout: 0, needs: null, line: 14 },
      {
        name: 'deploy',
        use: 'deploy-ecs',
        group: groups[0]?.id ?? '',
        max_parallel: 0,
        timeout: 600 * 1e9,
        needs: [{ step: 'copy-config', scope: 'service', line: 19 }],
        line: 17,
      },
    ],
  }
}

const backendDefinition = buildJobDefinition(
  'deploy-backend',
  'Backend and microservices',
  [{ id: 'backends', label: 'Backend services', services: ['backend-api'], line: 6 }],
  ['prod'],
)
const billingDefinition = buildJobDefinition(
  'deploy-billing',
  'Billing service only',
  [{ id: 'billing', label: 'billing', services: ['billing'], line: 6 }],
  [],
)
const portalDefinition = buildJobDefinition(
  'deploy-portal',
  'Portal services',
  [{ id: 'portal', label: 'Portal services', services: ['backend-api', 'billing'], line: 6 }],
  [],
)

/** A job file as it would sit in git, written from the definition so the two agree. */
function jobYaml(definition: JobDefinition): string {
  const groups = (definition.groups ?? []).flatMap((group) => [
    `  ${group.id}:`,
    `    label: ${group.label}`,
    `    services: [${(group.services ?? []).join(', ')}]`,
  ])
  const approval = definition.approval ? ['approval:', '  when:', `    labels: [${(definition.approval.labels ?? []).join(', ')}]`] : []
  return [
    'apiVersion: yumdrake/v1',
    'kind: Job',
    `job: ${definition.id}`,
    `title: "${definition.title}"`,
    '',
    'environment:',
    `  labels: [${(definition.labels ?? []).join(', ')}]`,
    '',
    'groups:',
    ...groups,
    '',
    ...approval,
    '',
  ].join('\n')
}

const recent = t('2026-11-27T09:00:00Z')

/** A job as the mock keeps it; the list counts (services, approval labels) are derived from the definition when it is served. */
export type StoredJob = Omit<JobDetail, 'services' | 'approval_labels'>

export const jobsFixture: StoredJob[] = [
  {
    id: 1,
    job_id: 'deploy-backend',
    title: 'Backend and microservices',
    folder_id: 1,
    folder: 'Backend',
    repo: 'example-org/deploy-jobs',
    branch: 'main',
    path: 'jobs/deploy-backend.yaml',
    runnable: true,
    commit: '1111111111111111111111111111111111111111',
    synced_at: t('2026-11-27T08:30:00Z'),
    checked_at: recent,
    problem_count: 0,
    sync_error: '',
    sync_error_at: null,
    definition: backendDefinition,
    source: jobYaml(backendDefinition),
    problems: [],
    problems_commit: '',
  },
  {
    id: 2,
    job_id: 'deploy-billing',
    title: 'Billing service only',
    folder_id: 1,
    folder: 'Backend',
    repo: 'example-org/deploy-jobs',
    branch: 'main',
    path: 'jobs/deploy-billing.yaml',
    runnable: true,
    commit: '2222222222222222222222222222222222222222',
    synced_at: t('2026-11-27T08:30:00Z'),
    checked_at: recent,
    problem_count: 0,
    sync_error: '',
    sync_error_at: null,
    definition: billingDefinition,
    source: jobYaml(billingDefinition),
    problems: [],
    problems_commit: '',
  },
  {
    // A newer commit has problems; the last good definition is still in use, so the job stays runnable.
    id: 3,
    job_id: 'deploy-portal',
    title: 'Portal services',
    folder_id: 2,
    folder: 'Portal',
    repo: 'example-org/portal',
    branch: 'main',
    path: 'deploy/yumdrake.yaml',
    runnable: true,
    commit: '3333333333333333333333333333333333333333',
    synced_at: t('2026-11-26T16:10:00Z'),
    checked_at: recent,
    problem_count: 2,
    sync_error: '',
    sync_error_at: null,
    definition: portalDefinition,
    source: jobYaml(portalDefinition),
    problems: [
      { file: 'deploy/yumdrake.yaml', line: 17, message: 'step "deploy" needs "copy-configs", which does not exist' },
      { file: 'deploy/yumdrake.yaml', line: 23, message: 'group "portal-web" is not defined' },
    ],
    problems_commit: '4444444444444444444444444444444444444444',
  },
  {
    // Never read successfully: no definition, so it cannot be started; the reason is the sync error.
    id: 4,
    job_id: '',
    title: '',
    folder_id: 3,
    folder: 'Releases',
    repo: 'example-org/platform',
    branch: 'main',
    path: 'release/yumdrake.yaml',
    runnable: false,
    commit: '',
    synced_at: null,
    checked_at: recent,
    problem_count: 0,
    sync_error: 'repository example-org/platform was not found or the connection has no access to it',
    sync_error_at: recent,
    definition: null,
    source: '',
    problems: [],
    problems_commit: '',
  },
]

// ---- Access ----------------------------------------------------------------------

export const groupsFixture: AdminGroup[] = [
  { id: 1, name: 'Administrators', admin: true, member_count: 1 },
  { id: 2, name: 'QA', admin: false, member_count: 2 },
]

export const usersFixture: AdminUser[] = [
  {
    id: 1,
    email: 'dana@example.com',
    name: 'Dana Levi',
    source: 'saml',
    disabled: false,
    groups: [{ id: 1, name: 'Administrators', source: 'manual' }],
    last_sign_in_at: t('2026-11-27T08:41:00Z'),
    created_at: t('2026-10-01T10:00:00Z'),
  },
  {
    id: 3,
    email: 'omer@example.com',
    name: 'Omer Katz',
    source: 'saml',
    disabled: false,
    groups: [{ id: 2, name: 'QA', source: 'manual' }],
    last_sign_in_at: t('2026-11-27T11:20:00Z'),
    created_at: t('2026-10-02T10:00:00Z'),
  },
  {
    id: 4,
    email: 'ci-pipeline@example.com',
    name: 'ci-pipeline',
    source: 'service',
    disabled: false,
    groups: [{ id: 2, name: 'QA', source: 'manual' }],
    last_sign_in_at: null,
    created_at: t('2026-10-05T10:00:00Z'),
  },
]

// Everyone the Me presets stand for is also a row here, so the Users screen can show them.
const presetUsers: AdminUser[] = [viewerMe, jobManagerMe, accessManagerMe, connectionsManagerMe, auditorMe].map((me) => ({
  id: me.id,
  email: me.email,
  name: me.name,
  source: 'saml',
  disabled: false,
  // Lior is in QA only through single sign-on; the other people have no groups.
  groups: me.id === viewerMe.id ? [{ id: 2, name: 'QA', source: 'idp' }] : [],
  last_sign_in_at: null,
  created_at: t('2026-10-06T10:00:00Z'),
}))
usersFixture.push(...presetUsers)

export const grantsFixture: AdminGrant[] = [
  { id: 1, group_id: 2, action: 'run', label: 'dev', environment: '', folder_id: 0 },
  { id: 2, group_id: 2, action: 'view_logs', label: 'dev', environment: '', folder_id: 0 },
  { id: 3, group_id: 2, action: 'approve', label: 'preprod', environment: '', folder_id: 2 },
]

export const mappingsFixture: IdpMapping[] = [
  { idp_group: 'eng-devops', group_id: 1 },
  { idp_group: 'eng-qa', group_id: 2 },
]

export const auditFixture: AuditEntry[] = [
  {
    id: 5,
    at: t('2026-11-27T11:20:00Z'),
    actor: 'omer@example.com',
    actor_id: 3,
    action: 'auth.login',
    target_type: 'user',
    target_id: '3',
    detail: { via: 'saml' },
  },
  {
    id: 4,
    at: t('2026-11-27T09:10:00Z'),
    actor: 'dana@example.com',
    actor_id: 1,
    action: 'grant.create',
    target_type: 'grant',
    target_id: '3',
    detail: { group: 'QA', action: 'approve', label: 'preprod' },
  },
  {
    id: 3,
    at: t('2026-11-27T08:45:00Z'),
    actor: 'dana@example.com',
    actor_id: 1,
    action: 'job.register',
    target_type: 'job',
    target_id: '3',
    detail: { repo: 'example-org/portal', path: 'deploy/yumdrake.yaml' },
  },
  {
    id: 2,
    at: t('2026-11-26T17:00:00Z'),
    actor: 'dana@example.com',
    actor_id: 1,
    action: 'connection.create',
    target_type: 'github_connection',
    target_id: '1',
    detail: { owner: 'example-org', method: 'app' },
  },
  {
    id: 1,
    at: t('2026-11-26T16:30:00Z'),
    actor: 'system',
    actor_id: null,
    action: 'inventory.sync',
    target_type: 'inventory',
    target_id: '',
    detail: { commit: 'a1b2c3d4e5f6' },
  },
]

export const githubConnectionsFixture: GithubConnection[] = [
  { id: 1, owner: 'example-org', method: 'app', app_id: 123456, installation_id: 7890123, created_at: t('2026-10-01T10:30:00Z') },
  { id: 2, owner: 'example-labs', method: 'token', app_id: 0, installation_id: 0, created_at: t('2026-10-12T14:00:00Z') },
]

export const awsAccountsFixture: AwsAccount[] = [
  {
    id: 1,
    name: 'Development',
    account_id: '111111111111',
    discovery: { method: 'role', role_arn: 'arn:aws:iam::111111111111:role/yumdrake-discovery', external_id: 'yumdrake-dev', access_key_id_last4: '' },
    deploy: { method: 'role', role_arn: 'arn:aws:iam::111111111111:role/yumdrake-deploy', external_id: 'yumdrake-dev', access_key_id_last4: '' },
    environments: ['hetzner-01', 'hetzner-02', 'hetzner-03'],
    created_at: t('2026-10-02T09:00:00Z'),
  },
  {
    id: 2,
    name: 'Production',
    account_id: '333333333333',
    discovery: { method: 'key', role_arn: '', external_id: '', access_key_id_last4: 'Q7XA' },
    deploy: { method: 'key', role_arn: '', external_id: '', access_key_id_last4: 'M2PD' },
    environments: ['prod-01'],
    created_at: t('2026-10-03T09:00:00Z'),
  },
]

export const ssoFixture: SsoSettings = {
  enabled: true,
  idp_metadata_xml: '<EntityDescriptor entityID="https://idp.example.com/metadata"></EntityDescriptor>',
  entity_id: 'https://yumdrake.example.com/saml/metadata',
  groups_attribute: 'groups',
  email_attribute: '',
  name_attribute: 'displayName',
  acs_url: 'https://yumdrake.example.com/saml/acs',
  metadata_url: 'https://yumdrake.example.com/saml/metadata',
  entity_id_default: 'https://yumdrake.example.com/saml/metadata',
}

export const tokensFixture: Record<number, ApiToken[]> = {
  1: [
    {
      id: 1,
      name: 'laptop cli',
      created_at: t('2026-11-01T10:00:00Z'),
      expires_at: t('2027-01-30T10:00:00Z'),
      last_used_at: t('2026-11-26T12:00:00Z'),
    },
  ],
}

// ---- Freezes and Slack (Plan 7) ----------------------------------------------------------------

/** One active freeze (every prod run) and one that has ended (a database migration in preprod-01). */
export const freezesFixture: Freeze[] = [
  {
    id: 1,
    label: 'prod',
    environment: '',
    reason: 'Quarter-end change freeze',
    created_by_name: 'Dana Levi',
    created_at: t('2026-11-26T09:00:00Z'),
    expires_at: t('2026-11-30T18:00:00Z'),
    ended_at: null,
    ended_by_name: '',
  },
  {
    id: 2,
    label: '',
    environment: 'preprod-01',
    reason: 'Database migration',
    created_by_name: 'Ron Shapira',
    created_at: t('2026-11-25T08:00:00Z'),
    expires_at: t('2026-11-25T20:00:00Z'),
    ended_at: t('2026-11-25T14:30:00Z'),
    ended_by_name: 'Ron Shapira',
  },
]

export const slackFixture: SlackSettings = { token_set: true, set_at: t('2026-11-20T10:00:00Z'), set_by_name: 'Dana Levi' }

// ---- The mutable state of the mock server ------------------------------------------

export interface PendingDevice {
  userCode: string
  confirmedBy: Me | null
  tokenIssued: boolean
}

export interface MockState {
  /** The person the mock server believes is signed in (the session cookie's stand-in); null when signed out. */
  session: Me | null
  inventory: Inventory
  inventorySource: { repo: string; branch: string; path: string }
  services: Service[]
  folders: Folder[]
  jobs: StoredJob[]
  users: AdminUser[]
  groups: AdminGroup[]
  grants: AdminGrant[]
  mappings: IdpMapping[]
  audit: AuditEntry[]
  githubConnections: GithubConnection[]
  awsAccounts: AwsAccount[]
  sso: SsoSettings
  tokens: Record<number, ApiToken[]>
  /** Every run in full; the list and the stream views are derived from these. */
  runs: RunDetail[]
  /** The redacted manifest of each run, by run id; a run without one answers 404 on /plan. */
  plans: Record<number, Plan>
  /** The log of each run, by run id (any order; the mock sorts by time, leg and seq when it serves them). */
  logs: Record<number, ApiLogLine[]>
  /** Holes the mock reports in a run's log, by run id. */
  logGaps: Record<number, ApiLogGap[]>
  freezes: Freeze[]
  slack: SlackSettings
  devices: Record<string, PendingDevice>
  nextId: Record<string, number>
}

export function createMockState(): MockState {
  return structuredClone({
    session: adminMe as Me | null,
    inventory: inventoryFixture,
    inventorySource: { repo: 'example-org/deploy-config', branch: 'main', path: 'inventory' },
    services: servicesFixture,
    folders: foldersFixture,
    jobs: jobsFixture,
    users: usersFixture,
    groups: groupsFixture,
    grants: grantsFixture,
    mappings: mappingsFixture,
    audit: auditFixture,
    githubConnections: githubConnectionsFixture,
    awsAccounts: awsAccountsFixture,
    sso: ssoFixture,
    tokens: tokensFixture,
    runs: runsFixtureList,
    plans: plansFixture,
    logs: logsFixture,
    logGaps: {},
    freezes: freezesFixture,
    slack: slackFixture,
    devices: {},
    nextId: { folder: 4, job: 5, user: 10, group: 3, grant: 4, audit: 6, connection: 3, aws: 3, token: 2, run: 110, freeze: 3 },
  })
}
