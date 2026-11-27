import { describe, expect, it } from 'vitest'
import { api } from '../api/client'
import type { ApiError } from '../api/error'
import type { JobDetail, JobSummary, Me, Service } from '../api/types'
import {
  accessManagerMe,
  adminMe,
  auditorMe,
  connectionsManagerMe,
  jobManagerMe,
  viewerMe,
} from './fixtures'
import { mockState, signInAs, signOut } from './server'

interface Route {
  method: string
  path: string
  body?: unknown
  /** The person (besides admin) who may call it, or 'anyone' / 'public'. */
  allowed: Me | 'anyone' | 'public'
  success: number
}

// Every route of the contract once, so a screen's test can rely on the mock server answering it.
const routes: Route[] = [
  { method: 'GET', path: '/api/v1/auth/methods', allowed: 'public', success: 200 },
  { method: 'GET', path: '/api/v1/me', allowed: 'anyone', success: 200 },
  { method: 'GET', path: '/api/v1/inventory', allowed: 'anyone', success: 200 },
  { method: 'GET', path: '/api/v1/inventory/services/billing', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/inventory/sync', allowed: connectionsManagerMe, success: 200 },
  { method: 'PUT', path: '/api/v1/admin/inventory-source', body: { repo: 'o/r', branch: 'main', path: 'inv' }, allowed: connectionsManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/admin/github-connections', allowed: connectionsManagerMe, success: 200 },
  { method: 'POST', path: '/api/v1/admin/github-connections', body: { owner: 'new-org', method: 'token', token: 'x' }, allowed: connectionsManagerMe, success: 201 },
  { method: 'GET', path: '/api/v1/admin/aws-accounts', allowed: connectionsManagerMe, success: 200 },
  {
    method: 'POST',
    path: '/api/v1/admin/aws-accounts',
    body: {
      name: 'Staging',
      account_id: '444444444444',
      discovery: { method: 'role', role_arn: 'arn:aws:iam::444444444444:role/d' },
      deploy: { method: 'key', access_key_id: 'AKIAEXAMPLE1234', secret_access_key: 's' },
    },
    allowed: connectionsManagerMe,
    success: 201,
  },
  { method: 'POST', path: '/api/v1/admin/aws-accounts/1/test', allowed: connectionsManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/folders', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/folders', body: { name: 'Tools' }, allowed: jobManagerMe, success: 201 },
  { method: 'GET', path: '/api/v1/jobs', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/jobs', body: { folder_id: 1, repo: 'o/r', branch: 'main', path: 'jobs/x.yaml' }, allowed: jobManagerMe, success: 201 },
  { method: 'GET', path: '/api/v1/jobs/3', allowed: 'anyone', success: 200 },
  { method: 'PATCH', path: '/api/v1/jobs/1', body: { folder_id: 2 }, allowed: jobManagerMe, success: 200 },
  { method: 'POST', path: '/api/v1/jobs/1/sync', allowed: jobManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/admin/users', allowed: accessManagerMe, success: 200 },
  { method: 'POST', path: '/api/v1/admin/users', body: { email: 'n@example.com', name: 'N', source: 'local', password: 'a-long-password-1' }, allowed: accessManagerMe, success: 201 },
  { method: 'PATCH', path: '/api/v1/admin/users/3', body: { disabled: true }, allowed: accessManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/admin/groups', allowed: accessManagerMe, success: 200 },
  { method: 'POST', path: '/api/v1/admin/groups', body: { name: 'Ops' }, allowed: accessManagerMe, success: 201 },
  { method: 'PATCH', path: '/api/v1/admin/groups/2', body: { admin: false }, allowed: accessManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/admin/idp-mappings', allowed: accessManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/admin/idp-mappings/eng-qa', allowed: accessManagerMe, success: 200 },
  { method: 'PUT', path: '/api/v1/admin/idp-mappings/eng-ops', body: { group_id: 2 }, allowed: accessManagerMe, success: 200 },
  { method: 'DELETE', path: '/api/v1/admin/idp-mappings/eng-qa', allowed: accessManagerMe, success: 204 },
  { method: 'GET', path: '/api/v1/admin/grants', allowed: accessManagerMe, success: 200 },
  { method: 'POST', path: '/api/v1/admin/grants', body: { group_id: 2, action: 'run', label: 'dev' }, allowed: accessManagerMe, success: 201 },
  { method: 'DELETE', path: '/api/v1/admin/grants/1', allowed: accessManagerMe, success: 204 },
  { method: 'GET', path: '/api/v1/admin/audit?limit=2', allowed: auditorMe, success: 200 },
  { method: 'GET', path: '/api/v1/admin/sso', allowed: accessManagerMe, success: 200 },
  { method: 'PUT', path: '/api/v1/admin/sso', body: { enabled: false, idp_metadata_xml: '', entity_id: '', groups_attribute: 'groups', email_attribute: '', name_attribute: 'displayName' }, allowed: accessManagerMe, success: 200 },
  { method: 'POST', path: '/api/v1/admin/sso/test', allowed: accessManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/tokens', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/tokens', body: { name: 'ci' }, allowed: 'anyone', success: 201 },
  { method: 'DELETE', path: '/api/v1/tokens/1', allowed: 'anyone', success: 204 },
  { method: 'POST', path: '/api/v1/cli/login', allowed: 'public', success: 200 },
  { method: 'POST', path: '/api/v1/cli/login/confirm', body: { user_code: 'KQ7M2X11' }, allowed: 'anyone', success: 204 },
  { method: 'POST', path: '/api/v1/cli/login/token', body: { device_code: 'device-code-1' }, allowed: 'public', success: 200 },
]

async function statusOf(route: Route): Promise<number> {
  try {
    await api(route.method, route.path, route.body)
    return route.success
  } catch (error) {
    return (error as ApiError).status
  }
}

describe('the mock API', () => {
  it.each(routes)('answers $method $path as the admin', async (route) => {
    signInAs(adminMe)
    if (route.path.includes('/cli/login/') && !route.path.endsWith('/login')) {
      await api('POST', '/api/v1/cli/login') // the device codes the next calls refer to
    }
    expect(await statusOf(route)).toBe(route.success)
  })

  it.each(routes.filter((route) => route.allowed !== 'public'))('answers $method $path with 401 when signed out', async (route) => {
    signOut()
    expect(await statusOf(route)).toBe(401)
  })

  it.each(routes.filter((route) => typeof route.allowed === 'object'))(
    'answers $method $path with 403 for a person without the grant and success with it',
    async (route) => {
      signInAs(viewerMe)
      expect(await statusOf(route)).toBe(403)
      signInAs(route.allowed as Me)
      expect(await statusOf(route)).toBe(route.success)
    },
  )

  it('refuses the last administrator being disabled', async () => {
    signInAs(adminMe)
    await expect(api('PATCH', '/api/v1/admin/users/1', { disabled: true })).rejects.toMatchObject({ status: 409, code: 'conflict' })
  })

  it('pages the audit log newest first', async () => {
    signInAs(auditorMe)
    const first = await api<{ entries: { id: number }[]; next_before: number | null }>('GET', '/api/v1/admin/audit?limit=2')
    expect(first.entries.map((entry) => entry.id)).toEqual([5, 4])
    expect(first.next_before).toBe(4)
    const last = await api<{ entries: { id: number }[]; next_before: number | null }>('GET', '/api/v1/admin/audit?limit=10&before=2')
    expect(last.entries.map((entry) => entry.id)).toEqual([1])
    expect(last.next_before).toBeNull()
  })

  it('sends null for the lists the Go server leaves nil (needs, env_files, tomls)', async () => {
    const service = await api<Service>('GET', '/api/v1/inventory/services/billing')
    expect(service.environments['hetzner-01']?.env_files).toBeNull()
    const backend = await api<Service>('GET', '/api/v1/inventory/services/backend-api')
    expect(backend.environments['hetzner-01']?.tomls).toBeNull()
    const job = await api<JobDetail>('GET', '/api/v1/jobs/1')
    expect(job.definition?.steps?.[0]?.needs).toBeNull()
  })

  it('answers the single IdP mapping and 404 for an unknown one', async () => {
    await expect(api('GET', '/api/v1/admin/idp-mappings/eng-qa')).resolves.toEqual({ idp_group: 'eng-qa', group_id: 2 })
    await expect(api('GET', '/api/v1/admin/idp-mappings/nope')).rejects.toMatchObject({ status: 404 })
  })

  it('lists every signed-in preset person as a user', async () => {
    signInAs(accessManagerMe)
    const { users } = await api<{ users: { email: string }[] }>('GET', '/api/v1/admin/users')
    for (const person of [viewerMe, jobManagerMe, accessManagerMe, connectionsManagerMe, auditorMe]) {
      expect(users.map((user) => user.email)).toContain(person.email)
    }
  })

  it('has one job with problems that is still runnable and one that cannot run', () => {
    const jobs = mockState().jobs
    expect(jobs.filter((job) => job.problem_count > 0)).toHaveLength(1)
    expect(jobs.filter((job) => !job.runnable)).toHaveLength(1)
    expect(mockState().inventory.rejected?.problems.length).toBeGreaterThan(0)
  })

  it('lists the services and approval labels of each job, and none for a job never read', async () => {
    signInAs(adminMe)
    const { jobs } = await api<{ jobs: JobSummary[] }>('GET', '/api/v1/jobs')
    const byId = (id: number) => jobs.find((job) => job.id === id)
    expect(byId(1)).toMatchObject({ services: ['backend-api'], approval_labels: ['prod'] })
    expect(byId(3)).toMatchObject({ services: ['backend-api', 'billing'], approval_labels: [] })
    expect(byId(4)?.services).toBeUndefined()
    expect(byId(4)?.approval_labels).toBeUndefined()
  })
})
