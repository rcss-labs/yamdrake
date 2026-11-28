// A complete in-memory stand-in for the Yumdrake API, for tests. It answers every route of the contract,
// enforces permissions the way the server does (401 when signed out, 403 when the grant is missing) and
// keeps state that changes as tests create, update and delete things. Everything resets before each test.
//
// Helpers for tests (all re-exported from ./server):
//   signInAs(me) / signOut()            who the mock server believes is signed in
//   failNext(method, path, status, code, message?, headers?)   the next matching request fails once
//   delayNext(method, path, ms)         the next matching request answers after ms
//   requests                            every request the server saw, in order
//   mockState()                         the live state, for arranging or checking things
//   resetMockApi()                      done for you in setup.ts after each test
import { http, HttpResponse } from 'msw'
import type { HttpHandler } from 'msw'
import { can } from '../auth/permissions'
import type { Target } from '../auth/permissions'
import { ALL_ACTIONS } from '../api/types'
import type {
  Action,
  AdminGroup,
  AdminUser,
  AuditEntry,
  AwsAccount,
  AwsCredentialInput,
  AwsCredentialView,
  CreatedToken,
  ErrorBody,
  GithubConnection,
  Inventory,
  JobDetail,
  JobSummary,
  Me,
} from '../api/types'
import { buildJobDefinition, createMockState, knownPassword, mePresets } from './fixtures'
import type { MockState, StoredJob } from './fixtures'

// ---- State, request log, injected failures and delays ------------------------------

let state: MockState = createMockState()

export interface SeenRequest {
  method: string
  /** The concrete path, for example "/api/v1/jobs/3". */
  path: string
  /** The query string including "?", or "". */
  search: string
  /** The parsed JSON body for requests that had one, otherwise undefined. */
  body: unknown
  /** The status the mock server answered with; undefined while it is still working on it. */
  status: number | undefined
}

/** Every request the mock server has seen since the last reset. */
export const requests: SeenRequest[] = []

interface InjectedFailure {
  status: number
  code: string
  message: string
  headers: Record<string, string>
}

const failures = new Map<string, InjectedFailure[]>()
const delays = new Map<string, number[]>()

const keyOf = (method: string, path: string) => `${method.toUpperCase()} ${path}`

/** The state behind the mock API; change it to arrange a scenario, read it to check a result. */
export function mockState(): MockState {
  return state
}

/** Puts everything back to the fixtures, signed in as the admin. Called after every test by setup.ts. */
export function resetMockApi(): void {
  state = createMockState()
  requests.length = 0
  failures.clear()
  delays.clear()
}

/** From now on the mock server treats this person as signed in (the same as having a valid session cookie). */
export function signInAs(me: Me): void {
  state.session = structuredClone(me)
}

/** From now on every route that needs a session answers 401. */
export function signOut(): void {
  state.session = null
}

/**
 * The next request with this method and path answers with this error, once, before any permission check.
 * `path` is the concrete path without the query, for example "/api/v1/jobs/3".
 */
export function failNext(
  method: string,
  path: string,
  status: number,
  code: string,
  message = 'Simulated failure',
  headers: Record<string, string> = {},
): void {
  const key = keyOf(method, path)
  failures.set(key, [...(failures.get(key) ?? []), { status, code, message, headers }])
}

/** The next request with this method and path is answered after `ms` milliseconds (real or fake timers). */
export function delayNext(method: string, path: string, ms: number): void {
  const key = keyOf(method, path)
  delays.set(key, [...(delays.get(key) ?? []), ms])
}

// ---- Small helpers -------------------------------------------------------------------

const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  HttpResponse.json(body as Record<string, unknown>, { status, headers })

const noContent = () => new HttpResponse(null, { status: 204 })

function errorResponse(status: number, code: string, message: string, headers: Record<string, string> = {}) {
  const body: ErrorBody = { error: { code, message } }
  return HttpResponse.json(body, { status, headers })
}

const invalid = (message: string) => errorResponse(400, 'invalid', message)
const notFound = (what: string) => errorResponse(404, 'not_found', `${what} not found`)
const conflict = (message: string) => errorResponse(409, 'conflict', message)

type Fields = Record<string, unknown>

function fields(body: unknown): Fields {
  return typeof body === 'object' && body !== null ? (body as Fields) : {}
}
const text = (value: unknown): string => (typeof value === 'string' ? value : '')
const whole = (value: unknown): number => (typeof value === 'number' && Number.isFinite(value) ? value : 0)

function takeId(prefix: string): number {
  const id = state.nextId[prefix] ?? 1
  state.nextId[prefix] = id + 1
  return id
}

const now = () => '2026-11-27T12:00:00Z'

function recordAudit(action: string, targetType: string, targetId: string | number, detail: Record<string, unknown> = {}) {
  const entry: AuditEntry = {
    id: takeId('audit'),
    at: now(),
    actor: state.session?.email ?? 'system',
    actor_id: state.session?.id ?? null,
    action,
    target_type: targetType,
    target_id: String(targetId),
    detail,
  }
  state.audit.unshift(entry)
}

// ---- Routes ---------------------------------------------------------------------------

type Guard = 'public' | 'session' | { action: Action; target?: (ctx: Context) => Target }

interface Params {
  id: string
  name: string
  idpGroup: string
}

interface Context {
  me: Me | null
  params: Params
  url: URL
  body: unknown
}

type Resolver = (ctx: Context) => Response | Promise<Response>

const factories = { GET: http.get, POST: http.post, PUT: http.put, PATCH: http.patch, DELETE: http.delete }

function route(method: keyof typeof factories, path: string, guard: Guard, resolver: Resolver): HttpHandler {
  return factories[method](path, async ({ request, params }) => {
    const url = new URL(request.url)
    let body: unknown
    if (method !== 'GET') {
      const raw = await request.text()
      try {
        body = raw === '' ? undefined : JSON.parse(raw)
      } catch {
        body = raw
      }
    }
    const seen: SeenRequest = { method, path: url.pathname, search: url.search, body, status: undefined }
    requests.push(seen)
    const finish = (response: Response) => {
      seen.status = response.status
      return response
    }

    const key = keyOf(method, url.pathname)
    const waits = delays.get(key)
    const wait = waits?.shift()
    if (wait !== undefined) {
      await new Promise((resolve) => setTimeout(resolve, wait))
    }
    const failure = failures.get(key)?.shift()
    if (failure) {
      return finish(errorResponse(failure.status, failure.code, failure.message, failure.headers))
    }

    const me = state.session
    const context: Context = {
      me,
      params: { id: String(params.id ?? ''), name: String(params.name ?? ''), idpGroup: String(params.idpGroup ?? '') },
      url,
      body,
    }
    if (guard !== 'public') {
      if (me === null) {
        return finish(errorResponse(401, 'unauthorized', 'You are not signed in'))
      }
      if (guard !== 'session') {
        const target = guard.target ? guard.target(context) : {}
        if (!can(me, guard.action, target)) {
          return finish(errorResponse(403, 'forbidden', 'You do not have permission to do this'))
        }
      }
    }
    return finish(await resolver(context))
  })
}

const view = { action: 'view' } as const
const manageJobs = (target?: (ctx: Context) => Target): Guard => ({ action: 'manage_jobs', target })
const manageConnections: Guard = { action: 'manage_connections' }
const manageAccess: Guard = { action: 'manage_access' }

// ---- Derived views ----------------------------------------------------------------------

function jobCounts(job: StoredJob): Pick<JobSummary, 'services' | 'approval_labels'> {
  const services = new Set((job.definition?.groups ?? []).flatMap((group) => group.services ?? []))
  return { services: services.size, approval_labels: job.definition?.approval?.labels ?? [] }
}

function jobSummary(job: StoredJob): JobSummary {
  const { definition: _definition, source: _source, problems: _problems, problems_commit: _problemsCommit, ...summary } = job
  void _definition
  void _source
  void _problems
  void _problemsCommit
  return { ...summary, ...jobCounts(job) }
}

function jobDetail(job: StoredJob): JobDetail {
  return { ...job, ...jobCounts(job) }
}

function findJob(idText: string): StoredJob | undefined {
  return state.jobs.find((job) => String(job.id) === idText)
}

function userView(user: AdminUser): AdminUser {
  return user
}

function groupView(group: AdminGroup): AdminGroup {
  return { ...group, member_count: state.users.filter((user) => user.groups.some((ref) => ref.id === group.id)).length }
}

/** At least one enabled user in an admin group must remain (Plan 3, Task 6). */
function hasAdministratorLeft(): boolean {
  const adminGroupIds = new Set(state.groups.filter((group) => group.admin).map((group) => group.id))
  return state.users.some((user) => !user.disabled && user.groups.some((ref) => adminGroupIds.has(ref.id)))
}

function awsView(input: AwsCredentialInput): AwsCredentialView {
  return {
    method: input.method,
    role_arn: input.method === 'role' ? (input.role_arn ?? '') : '',
    external_id: input.method === 'role' ? (input.external_id ?? '') : '',
    access_key_id_last4: input.method === 'key' ? (input.access_key_id ?? '').slice(-4) : '',
  }
}

function awsCredentialProblem(label: string, input: unknown): string {
  const credential = fields(input)
  if (credential.method === 'role') {
    return text(credential.role_arn) === '' ? `${label} needs a role ARN` : ''
  }
  if (credential.method === 'key') {
    return text(credential.access_key_id) === '' || text(credential.secret_access_key) === ''
      ? `${label} needs an access key id and a secret access key`
      : ''
  }
  return `${label} method must be "role" or "key"`
}

function syncInventoryNow(): Inventory {
  state.inventory.checked_at = now()
  return state.inventory
}

const ownerPattern = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/
const validRepo = (repo: string) => /^[^\s/]+\/[^\s/]+$/.test(repo)

// ---- The handlers --------------------------------------------------------------------------

export const handlers: HttpHandler[] = [
  // Sign-in
  route('GET', '/api/v1/auth/methods', 'public', () => json({ password: true, saml: state.sso.enabled })),

  route('POST', '/api/v1/auth/login', 'public', ({ body }) => {
    const { email, password } = fields(body)
    const person = mePresets.find((preset) => preset.email === email)
    const disabled = state.users.some((user) => user.email === email && user.disabled)
    if (!person || password !== knownPassword || disabled) {
      return errorResponse(401, 'unauthorized', 'email or password is not correct')
    }
    state.session = structuredClone(person)
    return json(person)
  }),

  // Like the server, a sign-out needs a session; the web treats the 401 for none as signed out.
  route('POST', '/api/v1/auth/logout', 'session', () => {
    state.session = null
    return noContent()
  }),

  route('GET', '/api/v1/me', 'session', ({ me }) => json(me)),

  // Inventory
  route('GET', '/api/v1/inventory', view, () => json(state.inventory)),

  route('GET', '/api/v1/inventory/services/:name', view, ({ params }) => {
    const service = state.services.find((candidate) => candidate.name === params.name)
    return service ? json(service) : notFound('service')
  }),

  route('POST', '/api/v1/inventory/sync', manageConnections, () => json(syncInventoryNow())),

  route('GET', '/api/v1/admin/inventory-source', manageConnections, () => json(state.inventorySource)),

  route('PUT', '/api/v1/admin/inventory-source', manageConnections, ({ body }) => {
    const { repo, branch, path } = fields(body)
    if (!validRepo(text(repo))) {
      return invalid(`repo "${text(repo)}" must look like owner/name`)
    }
    if (text(branch).trim() === '') {
      return invalid('branch is required')
    }
    state.inventorySource = { repo: text(repo), branch: text(branch), path: text(path) }
    recordAudit('inventory.source', 'inventory', '', { repo: text(repo), branch: text(branch), path: text(path) })
    return json(syncInventoryNow())
  }),

  // GitHub connections
  route('GET', '/api/v1/admin/github-connections', manageConnections, () =>
    json({ connections: state.githubConnections }),
  ),

  route('POST', '/api/v1/admin/github-connections', manageConnections, ({ body }) => {
    const request = fields(body)
    const owner = text(request.owner).trim().toLowerCase()
    if (!ownerPattern.test(owner)) {
      return invalid(`owner "${text(request.owner)}" is not a valid GitHub user or organisation name`)
    }
    const method = text(request.method)
    const appId = whole(request.app_id)
    const installationId = whole(request.installation_id)
    if (method === 'token') {
      if (text(request.token) === '') {
        return invalid('method "token" requires token')
      }
      if (appId !== 0 || installationId !== 0 || text(request.private_key) !== '') {
        return invalid('method "token" takes only token')
      }
    } else if (method === 'app') {
      if (appId <= 0 || installationId <= 0 || text(request.private_key) === '') {
        return invalid('method "app" requires app_id, installation_id and private_key')
      }
      if (text(request.token) !== '') {
        return invalid('method "app" takes no token')
      }
      if (!text(request.private_key).includes('BEGIN')) {
        return invalid('private_key is not a valid RSA private key in PEM form')
      }
    } else {
      return invalid('method must be "token" or "app"')
    }
    if (state.githubConnections.some((connection) => connection.owner === owner)) {
      return conflict(`owner "${owner}" already has a connection`)
    }
    const created: GithubConnection = {
      id: takeId('connection'),
      owner,
      method,
      app_id: appId,
      installation_id: installationId,
      created_at: now(),
    }
    state.githubConnections.push(created)
    recordAudit('connection.create', 'github_connection', created.id, { owner, method })
    return json(created, 201)
  }),

  // AWS accounts
  route('GET', '/api/v1/admin/aws-accounts', manageConnections, () => json({ accounts: state.awsAccounts })),

  route('POST', '/api/v1/admin/aws-accounts', manageConnections, ({ body }) => {
    const request = fields(body)
    const name = text(request.name).trim()
    const accountId = text(request.account_id).trim()
    if (name === '') {
      return invalid('name is required')
    }
    if (!/^\d{12}$/.test(accountId)) {
      return invalid('account_id must be 12 digits')
    }
    const problem = awsCredentialProblem('discovery', request.discovery) || awsCredentialProblem('deploy', request.deploy)
    if (problem !== '') {
      return invalid(problem)
    }
    if (state.awsAccounts.some((account) => account.name === name || account.account_id === accountId)) {
      return conflict(`an account named "${name}" or with id ${accountId} already exists`)
    }
    const account: AwsAccount = {
      id: takeId('aws'),
      name,
      account_id: accountId,
      discovery: awsView(request.discovery as AwsCredentialInput),
      deploy: awsView(request.deploy as AwsCredentialInput),
      environments: state.inventory.environments.filter((env) => env.aws_account === accountId).map((env) => env.name),
      created_at: now(),
    }
    state.awsAccounts.push(account)
    recordAudit('aws_account.create', 'aws_account', account.id, { name, account_id: accountId })
    return json(account, 201)
  }),

  // An account id ending in 999 answers like a role in another account; every other account is fine.
  route('POST', '/api/v1/admin/aws-accounts/:id/test', manageConnections, ({ params }) => {
    const account = state.awsAccounts.find((candidate) => String(candidate.id) === params.id)
    if (!account) {
      return notFound('AWS account')
    }
    if (account.account_id.endsWith('999')) {
      return json({
        ok: false,
        error_code: 'AccountMismatch',
        message: `The credential belongs to another account than ${account.account_id}`,
      })
    }
    return json({ ok: true, error_code: '', message: `Connected to account ${account.account_id}` })
  }),

  // Folders
  route('GET', '/api/v1/folders', view, () => json({ folders: state.folders })),

  route(
    'POST',
    '/api/v1/folders',
    manageJobs(),
    ({ body }) => {
      const name = text(fields(body).name).trim()
      if (name === '' || name.length > 100) {
        return invalid('name is required and at most 100 characters')
      }
      if (state.folders.some((folder) => folder.name === name)) {
        return conflict(`a folder named "${name}" already exists`)
      }
      const folder = { id: takeId('folder'), name }
      state.folders.push(folder)
      recordAudit('folder.create', 'folder', folder.id, { name })
      return json(folder, 201)
    },
  ),

  // Jobs
  route('GET', '/api/v1/jobs', view, () => json({ jobs: state.jobs.map(jobSummary) })),

  route('GET', '/api/v1/jobs/:id', view, ({ params }) => {
    const job = findJob(params.id)
    return job ? json(jobDetail(job)) : notFound('job')
  }),

  // A path containing "broken" is read with problems and no good version (the job is not runnable).
  route(
    'POST',
    '/api/v1/jobs',
    manageJobs(({ body }) => ({ folderId: whole(fields(body).folder_id) })),
    ({ body }) => {
      const request = fields(body)
      const folderId = whole(request.folder_id)
      const repo = text(request.repo)
      const branch = text(request.branch)
      const path = text(request.path)
      if (folderId <= 0) {
        return invalid('folder_id is required')
      }
      if (!validRepo(repo)) {
        return invalid(`repo "${repo}" must look like owner/name`)
      }
      if (branch.trim() === '') {
        return invalid('branch is required')
      }
      if (path.trim() === '') {
        return invalid('path is required')
      }
      const folder = state.folders.find((candidate) => candidate.id === folderId)
      if (!folder) {
        return invalid(`folder ${folderId} does not exist`)
      }
      if (state.jobs.some((job) => job.repo === repo && job.branch === branch && job.path === path)) {
        return conflict(`${repo} on branch ${branch} at ${path} is already registered`)
      }
      const jobId = (path.split('/').pop() ?? path).replace(/\.ya?ml$/, '')
      const broken = path.includes('broken')
      const created: StoredJob = {
        id: takeId('job'),
        job_id: broken ? '' : jobId,
        title: broken ? '' : jobId,
        folder_id: folder.id,
        folder: folder.name,
        repo,
        branch,
        path,
        runnable: !broken,
        commit: broken ? '' : '5555555555555555555555555555555555555555',
        synced_at: broken ? null : now(),
        checked_at: now(),
        problem_count: broken ? 1 : 0,
        sync_error: '',
        sync_error_at: null,
        definition: broken
          ? null
          : buildJobDefinition(jobId, jobId, [{ id: 'all', label: 'All services', services: ['backend-api'], line: 6 }], []),
        source: broken ? '' : `apiVersion: yumdrake/v1\nkind: Job\njob: ${jobId}\n`,
        problems: broken ? [{ file: path, line: 3, message: 'labels must list at least one environment label' }] : [],
        problems_commit: broken ? '6666666666666666666666666666666666666666' : '',
      }
      state.jobs.push(created)
      recordAudit('job.register', 'job', created.id, { repo, branch, path })
      return json(jobDetail(created), 201)
    },
  ),

  route(
    'PATCH',
    '/api/v1/jobs/:id',
    manageJobs(({ params }) => ({ folderId: findJob(params.id)?.folder_id })),
    ({ params, body, me }) => {
      const job = findJob(params.id)
      if (!job) {
        return notFound('job')
      }
      const folderId = whole(fields(body).folder_id)
      if (folderId <= 0) {
        return invalid('folder_id is required')
      }
      const folder = state.folders.find((candidate) => candidate.id === folderId)
      if (!folder) {
        return invalid(`folder ${folderId} does not exist`)
      }
      if (me && !can(me, 'manage_jobs', { folderId })) {
        return errorResponse(403, 'forbidden', 'You do not have permission to do this')
      }
      job.folder_id = folder.id
      job.folder = folder.name
      recordAudit('job.move', 'job', job.id, { folder: folder.name })
      return json(jobDetail(job))
    },
  ),

  route(
    'POST',
    '/api/v1/jobs/:id/sync',
    manageJobs(({ params }) => ({ folderId: findJob(params.id)?.folder_id })),
    ({ params }) => {
      const job = findJob(params.id)
      if (!job) {
        return notFound('job')
      }
      job.checked_at = now()
      return json(jobDetail(job))
    },
  ),

  // Users
  route('GET', '/api/v1/admin/users', manageAccess, () => json({ users: state.users.map(userView) })),

  route('POST', '/api/v1/admin/users', manageAccess, ({ body }) => {
    const request = fields(body)
    const email = text(request.email).trim().toLowerCase()
    const name = text(request.name).trim()
    if (!email.includes('@') || name === '') {
      return invalid('email and name are required')
    }
    if (request.source !== 'local' && request.source !== 'service') {
      return invalid('source must be "local" or "service"')
    }
    if (text(request.password).length < 12) {
      return invalid('password must be at least 12 characters')
    }
    if (state.users.some((user) => user.email === email)) {
      return conflict(`a user with email "${email}" already exists`)
    }
    const user: AdminUser = {
      id: takeId('user'),
      email,
      name,
      source: request.source,
      disabled: false,
      groups: [],
      last_sign_in_at: null,
      created_at: now(),
    }
    state.users.push(user)
    recordAudit('user.create', 'user', user.id, { email, source: request.source })
    return json(user, 201)
  }),

  route('PATCH', '/api/v1/admin/users/:id', manageAccess, ({ params, body }) => {
    const user = state.users.find((candidate) => String(candidate.id) === params.id)
    if (!user) {
      return notFound('user')
    }
    const request = fields(body)
    const before = structuredClone(user)
    if (typeof request.disabled === 'boolean') {
      user.disabled = request.disabled
    }
    if (Array.isArray(request.group_ids)) {
      const ids = request.group_ids.map(Number)
      const unknown = ids.find((id) => !state.groups.some((group) => group.id === id))
      if (unknown !== undefined) {
        return invalid(`group ${unknown} does not exist`)
      }
      // Like the server, only the manual memberships are replaced; a group reached only through single sign-on stays.
      const manual = state.groups.filter((group) => ids.includes(group.id)).map((group) => ({ id: group.id, name: group.name, source: 'manual' as const }))
      const idpOnly = user.groups.filter((ref) => ref.source === 'idp' && !ids.includes(ref.id))
      user.groups = [...manual, ...idpOnly].sort((a, b) => a.id - b.id)
    }
    if (!hasAdministratorLeft()) {
      user.disabled = before.disabled
      user.groups = before.groups
      return conflict('at least one enabled user must remain in an admin group')
    }
    recordAudit('user.update', 'user', user.id, { disabled: user.disabled, groups: user.groups.map((ref) => ref.name) })
    return json(typeof request.disabled === 'boolean' && request.disabled ? { ...user, sessions_revoked: true } : user)
  }),

  // Groups
  route('GET', '/api/v1/admin/groups', manageAccess, () => json({ groups: state.groups.map(groupView) })),

  route('POST', '/api/v1/admin/groups', manageAccess, ({ body }) => {
    const request = fields(body)
    const name = text(request.name).trim()
    if (name === '') {
      return invalid('name is required')
    }
    if (state.groups.some((group) => group.name === name)) {
      return conflict(`a group named "${name}" already exists`)
    }
    const group: AdminGroup = { id: takeId('group'), name, admin: request.admin === true, member_count: 0 }
    state.groups.push(group)
    recordAudit('group.create', 'group', group.id, { name, admin: group.admin })
    return json(groupView(group), 201)
  }),

  route('PATCH', '/api/v1/admin/groups/:id', manageAccess, ({ params, body }) => {
    const group = state.groups.find((candidate) => String(candidate.id) === params.id)
    if (!group) {
      return notFound('group')
    }
    const before = group.admin
    group.admin = fields(body).admin === true
    if (!hasAdministratorLeft()) {
      group.admin = before
      return conflict('at least one enabled user must remain in an admin group')
    }
    recordAudit('group.update', 'group', group.id, { admin: group.admin })
    return json(groupView(group))
  }),

  // Identity provider group mappings
  route('GET', '/api/v1/admin/idp-mappings', manageAccess, () => json({ mappings: state.mappings })),

  route('GET', '/api/v1/admin/idp-mappings/:idpGroup', manageAccess, ({ params }) => {
    const mapping = state.mappings.find((candidate) => candidate.idp_group === decodeURIComponent(params.idpGroup))
    return mapping ? json(mapping) : notFound('mapping')
  }),

  route('PUT', '/api/v1/admin/idp-mappings/:idpGroup', manageAccess, ({ params, body }) => {
    const idpGroup = decodeURIComponent(params.idpGroup)
    const groupId = whole(fields(body).group_id)
    if (!state.groups.some((group) => group.id === groupId)) {
      return invalid(`group ${groupId} does not exist`)
    }
    const existing = state.mappings.find((mapping) => mapping.idp_group === idpGroup)
    if (existing) {
      existing.group_id = groupId
    } else {
      state.mappings.push({ idp_group: idpGroup, group_id: groupId })
    }
    recordAudit('idp_mapping.put', 'idp_mapping', idpGroup, { group_id: groupId })
    return json({ idp_group: idpGroup, group_id: groupId })
  }),

  route('DELETE', '/api/v1/admin/idp-mappings/:idpGroup', manageAccess, ({ params }) => {
    const idpGroup = decodeURIComponent(params.idpGroup)
    const index = state.mappings.findIndex((mapping) => mapping.idp_group === idpGroup)
    if (index < 0) {
      return notFound('mapping')
    }
    state.mappings.splice(index, 1)
    recordAudit('idp_mapping.delete', 'idp_mapping', idpGroup)
    return noContent()
  }),

  // Grants
  route('GET', '/api/v1/admin/grants', manageAccess, () => json({ grants: state.grants })),

  route('POST', '/api/v1/admin/grants', manageAccess, ({ body }) => {
    const request = fields(body)
    const action = text(request.action) as Action
    const groupId = whole(request.group_id)
    if (!ALL_ACTIONS.includes(action)) {
      return invalid(`action "${text(request.action)}" is not an action`)
    }
    if (!state.groups.some((group) => group.id === groupId)) {
      return invalid(`group ${groupId} does not exist`)
    }
    const grant = {
      id: takeId('grant'),
      group_id: groupId,
      action,
      label: text(request.label),
      environment: text(request.environment),
      folder_id: whole(request.folder_id),
    }
    state.grants.push(grant)
    recordAudit('grant.create', 'grant', grant.id, { group_id: groupId, action })
    return json(grant, 201)
  }),

  route('DELETE', '/api/v1/admin/grants/:id', manageAccess, ({ params }) => {
    const index = state.grants.findIndex((grant) => String(grant.id) === params.id)
    if (index < 0) {
      return notFound('grant')
    }
    state.grants.splice(index, 1)
    recordAudit('grant.delete', 'grant', params.id)
    return noContent()
  }),

  // Audit log
  route('GET', '/api/v1/admin/audit', { action: 'view_audit' }, ({ url }) => {
    const before = Number(url.searchParams.get('before') ?? '')
    const requested = Number(url.searchParams.get('limit') ?? '')
    const limit = Number.isFinite(requested) && requested > 0 ? Math.min(requested, 200) : 50
    const newestFirst = [...state.audit].sort((a, b) => b.id - a.id).filter((entry) => !(before > 0) || entry.id < before)
    const page = newestFirst.slice(0, limit)
    const hasMore = newestFirst.length > page.length
    return json({ entries: page, next_before: hasMore ? (page[page.length - 1]?.id ?? null) : null })
  }),

  // Single sign-on settings
  route('GET', '/api/v1/admin/sso', manageAccess, () => json(state.sso)),

  route('PUT', '/api/v1/admin/sso', manageAccess, ({ body }) => {
    const request = fields(body)
    const metadata = text(request.idp_metadata_xml)
    if (request.enabled === true && !metadata.includes('<EntityDescriptor')) {
      return invalid('the identity provider metadata is not valid SAML metadata')
    }
    state.sso = {
      ...state.sso,
      enabled: request.enabled === true,
      idp_metadata_xml: metadata,
      // Empty means the default, which follows the public URL.
      entity_id: text(request.entity_id) || state.sso.entity_id_default,
      groups_attribute: text(request.groups_attribute) || 'groups',
      email_attribute: text(request.email_attribute),
      name_attribute: text(request.name_attribute) || 'displayName',
    }
    recordAudit('sso.update', 'sso', '', { enabled: state.sso.enabled })
    return json(state.sso)
  }),

  route('POST', '/api/v1/admin/sso/test', manageAccess, () => {
    const ok = state.sso.idp_metadata_xml.includes('<EntityDescriptor')
    return json({
      ok,
      message: ok
        ? 'The metadata is valid and has a signing certificate and a sign-on endpoint'
        : 'The metadata could not be read',
    })
  }),

  // The caller's own tokens
  route('GET', '/api/v1/tokens', 'session', ({ me }) => json({ tokens: state.tokens[me?.id ?? 0] ?? [] })),

  route('POST', '/api/v1/tokens', 'session', ({ me, body }) => {
    const request = fields(body)
    const name = text(request.name).trim()
    if (name === '') {
      return invalid('name is required')
    }
    const days = Math.min(whole(request.ttl_days) || 90, 365)
    const id = takeId('token')
    const created: CreatedToken = {
      id,
      name,
      created_at: now(),
      expires_at: new Date(Date.parse(now()) + days * 86_400_000).toISOString(),
      last_used_at: null,
      plaintext: `ymd_mock${String(id).padStart(4, '0')}abcdefghijklmnopqrstuvwxyz0123456789`,
    }
    const { plaintext: _plaintext, ...stored } = created
    void _plaintext
    const userId = me?.id ?? 0
    state.tokens[userId] = [...(state.tokens[userId] ?? []), stored]
    recordAudit('token.create', 'token', id, { name })
    return json(created, 201)
  }),

  route('DELETE', '/api/v1/tokens/:id', 'session', ({ me, params }) => {
    const userId = me?.id ?? 0
    const own = state.tokens[userId] ?? []
    if (!own.some((token) => String(token.id) === params.id)) {
      return notFound('token')
    }
    state.tokens[userId] = own.filter((token) => String(token.id) !== params.id)
    recordAudit('token.revoke', 'token', params.id)
    return noContent()
  }),

  // CLI device sign-in
  route('POST', '/api/v1/cli/login', 'public', () => {
    const count = Object.keys(state.devices).length + 1
    const deviceCode = `device-code-${count}`
    const userCode = `KQ7M2X${String(10 + count)}`
    state.devices[deviceCode] = { userCode, confirmedBy: null, tokenIssued: false }
    return json({
      device_code: deviceCode,
      user_code: userCode,
      verify_url: `https://yumdrake.example.com/cli/confirm?code=${userCode}`,
      interval: 5,
      expires_in: 600,
    })
  }),

  route('POST', '/api/v1/cli/login/confirm', 'session', ({ me, body }) => {
    const userCode = text(fields(body).user_code).trim().toUpperCase()
    const device = Object.values(state.devices).find((candidate) => candidate.userCode === userCode)
    if (!device) {
      return notFound('code')
    }
    device.confirmedBy = me
    recordAudit('cli.login.confirm', 'user', me?.id ?? 0)
    return noContent()
  }),

  route('POST', '/api/v1/cli/login/token', 'public', ({ body }) => {
    const device = state.devices[text(fields(body).device_code)]
    // Like the server: an unknown, used or timed-out code is one answer, 400 expired_token.
    if (!device || device.tokenIssued) {
      return errorResponse(400, 'expired_token', 'the device code is unknown or has expired')
    }
    if (!device.confirmedBy) {
      return json({ status: 'pending', interval: 5 })
    }
    device.tokenIssued = true
    const id = takeId('token')
    const expiresAt = new Date(Date.parse(now()) + 90 * 86_400_000).toISOString()
    const owner = device.confirmedBy.id
    state.tokens[owner] = [...(state.tokens[owner] ?? []), { id, name: 'yumdrake CLI', created_at: now(), expires_at: expiresAt, last_used_at: null }]
    return json({ status: 'complete', token: `ymd_mockcli${String(id).padStart(4, '0')}abcdefghijklmnopqrstuvwxyz0123456789`, token_id: id, expires_at: expiresAt })
  }),
]
