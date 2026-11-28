import { describe, expect, it } from 'vitest'
import { api } from '../api/client'
import type { ApiError } from '../api/error'
import type { Action, JobDetail, JobSummary, Me, RunDetail, Service } from '../api/types'
import {
  accessManagerMe,
  adminMe,
  auditorMe,
  connectionsManagerMe,
  fakeHash,
  jobManagerMe,
  runIds,
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

/** A person who may do just this one action anywhere (the preset people have none of the run actions). */
const grantee = (action: Action): Me => ({
  id: 20,
  email: 'grantee@example.com',
  name: 'Gal Grantee',
  admin: false,
  grants: [{ action, label: '', environment: '', folder_id: 0 }],
})

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
  { method: 'PUT', path: '/api/v1/admin/sso', body: { enabled: false, idp_metadata_xml: '', entity_id: '', groups_attribute: 'groups', email_attribute: '', name_attribute: 'displayName' }, allowed: adminMe, success: 200 },
  { method: 'POST', path: '/api/v1/admin/sso/test', allowed: accessManagerMe, success: 200 },
  { method: 'GET', path: '/api/v1/tokens', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/tokens', body: { name: 'ci' }, allowed: 'anyone', success: 201 },
  { method: 'DELETE', path: '/api/v1/tokens/1', allowed: 'anyone', success: 204 },
  { method: 'POST', path: '/api/v1/auth/logout', allowed: 'anyone', success: 204 },
  { method: 'POST', path: '/api/v1/cli/login', allowed: 'public', success: 200 },
  { method: 'POST', path: '/api/v1/cli/login/confirm', body: { user_code: 'KQ7M2X11' }, allowed: 'anyone', success: 204 },
  { method: 'POST', path: '/api/v1/cli/login/token', body: { device_code: 'device-code-1' }, allowed: 'public', success: 200 },
  // Runs, approvals, freezes and Slack (Plans 4 to 7)
  { method: 'GET', path: '/api/v1/runs', allowed: 'anyone', success: 200 },
  { method: 'GET', path: '/api/v1/runs?state=failed&state=planned&limit=5', allowed: 'anyone', success: 200 },
  { method: 'GET', path: '/api/v1/runs/104', allowed: 'anyone', success: 200 },
  { method: 'GET', path: '/api/v1/runs/101/plan', allowed: 'anyone', success: 200 },
  { method: 'GET', path: '/api/v1/runs/109/log', allowed: grantee('view_logs'), success: 200 },
  { method: 'POST', path: '/api/v1/jobs/1/runs', body: { environment: 'hetzner-01', selection: { backends: { version: '1.9.4' } }, start: false }, allowed: grantee('run'), success: 201 },
  { method: 'GET', path: '/api/v1/jobs/1/versions?environment=hetzner-01', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/runs/101/start', body: { manifest_id: fakeHash(101) }, allowed: grantee('run'), success: 200 },
  { method: 'POST', path: '/api/v1/runs/101/cancel', allowed: grantee('cancel'), success: 200 },
  { method: 'POST', path: '/api/v1/runs/106/legs/10605/reconcile', body: { outcome: 'succeeded' }, allowed: grantee('restore'), success: 200 },
  { method: 'POST', path: '/api/v1/runs/106/legs/10605/check-again', allowed: grantee('restore'), success: 200 },
  { method: 'POST', path: '/api/v1/admin/runs/106/release-locks', allowed: adminMe, success: 200 },
  { method: 'POST', path: '/api/v1/runs/109/restore', body: {}, allowed: grantee('restore'), success: 201 },
  { method: 'POST', path: '/api/v1/runs/105/rerun-failed', allowed: grantee('run'), success: 201 },
  { method: 'GET', path: '/api/v1/approvals', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/runs/103/approve', body: { manifest_id: fakeHash(103) }, allowed: grantee('approve'), success: 200 },
  { method: 'POST', path: '/api/v1/runs/103/reject', body: {}, allowed: grantee('approve'), success: 200 },
  { method: 'GET', path: '/api/v1/freezes', allowed: 'anyone', success: 200 },
  { method: 'POST', path: '/api/v1/admin/freezes', body: { label: 'dev', reason: 'Demo day', expires_at: '2026-11-29T00:00:00Z' }, allowed: accessManagerMe, success: 201 },
  { method: 'DELETE', path: '/api/v1/admin/freezes/1', allowed: accessManagerMe, success: 204 },
  { method: 'GET', path: '/api/v1/admin/slack', allowed: connectionsManagerMe, success: 200 },
  { method: 'PUT', path: '/api/v1/admin/slack', body: { token: 'xoxb-example' }, allowed: connectionsManagerMe, success: 200 },
  { method: 'POST', path: '/api/v1/admin/slack/test', body: { channel: '#deployments' }, allowed: connectionsManagerMe, success: 200 },
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

  it('refuses PUT /api/v1/admin/sso to a manage_access holder who is not an admin, as the server does', async () => {
    signInAs(accessManagerMe)
    expect(await statusOf({ method: 'GET', path: '/api/v1/admin/sso', allowed: accessManagerMe, success: 200 })).toBe(200)
    expect(await statusOf({ method: 'PUT', path: '/api/v1/admin/sso', body: { enabled: false }, allowed: adminMe, success: 200 })).toBe(403)
  })

  it('walks a CLI sign-in the way the server answers it', async () => {
    const start = await api<{ device_code: string; user_code: string; expires_in: number; interval: number }>('POST', '/api/v1/cli/login')
    expect(start.expires_in).toBeGreaterThan(0)
    expect(await api('POST', '/api/v1/cli/login/token', { device_code: start.device_code })).toEqual({ status: 'pending', interval: start.interval })

    signInAs(adminMe)
    await api('POST', '/api/v1/cli/login/confirm', { user_code: start.user_code })
    signOut()
    const done = await api<Record<string, unknown>>('POST', '/api/v1/cli/login/token', { device_code: start.device_code })
    expect(done).toMatchObject({ status: 'complete', token: expect.any(String), token_id: expect.any(Number), expires_at: expect.any(String) })

    // A used or unknown code is an expired token, not a missing page.
    await expect(api('POST', '/api/v1/cli/login/token', { device_code: start.device_code })).rejects.toMatchObject({ status: 400, code: 'expired_token' })
    await expect(api('POST', '/api/v1/cli/login/token', { device_code: 'nope' })).rejects.toMatchObject({ status: 400, code: 'expired_token' })
  })

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

  it('sends null on the wire for the lists the Go server leaves nil (needs, env_files, tomls)', async () => {
    // Raw fetch: api() turns these nulls into [], and this guards that the mock still exercises that.
    const wire = async <T,>(path: string): Promise<T> => (await fetch(path)).json() as Promise<T>
    const service = await wire<Service>('/api/v1/inventory/services/billing')
    expect(service.environments['hetzner-01']?.env_files).toBeNull()
    const backend = await wire<Service>('/api/v1/inventory/services/backend-api')
    expect(backend.environments['hetzner-01']?.tomls).toBeNull()
    const job = await wire<JobDetail>('/api/v1/jobs/1')
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

  it('lists the services and approval labels of each job, and zero and none for a job never read', async () => {
    signInAs(adminMe)
    const { jobs } = await api<{ jobs: JobSummary[] }>('GET', '/api/v1/jobs')
    const byId = (id: number) => jobs.find((job) => job.id === id)
    expect(byId(1)).toMatchObject({ services: 1, approval_labels: ['prod'] })
    expect(byId(3)).toMatchObject({ services: 2, approval_labels: [] })
    expect(byId(4)).toMatchObject({ services: 0, approval_labels: [] })
  })

  it('sends null on the wire for the locks of a run that holds none', async () => {
    const run = (await (await fetch('/api/v1/runs/109')).json()) as { locks: unknown }
    expect(run.locks).toBeNull()
  })
})

describe('the mock run routes keep state and answer like the server', () => {
  const run = (id: number) => api<RunDetail>('GET', `/api/v1/runs/${id}`)

  it('starts a planned run once; a second start is a 409', async () => {
    signInAs(adminMe)
    expect((await api<RunDetail>('POST', `/api/v1/runs/${runIds.planned}/start`, { manifest_id: fakeHash(101) })).state).toBe('queued')
    expect((await run(runIds.planned)).state).toBe('queued')
    await expect(api('POST', `/api/v1/runs/${runIds.planned}/start`)).rejects.toMatchObject({ status: 409, code: 'conflict' })
  })

  it('sends a started run of an approval label to waiting_approval with a pending approval', async () => {
    signInAs(adminMe)
    const created = await api<RunDetail>('POST', '/api/v1/jobs/1/runs', { environment: 'prod-01', selection: { backends: { version: '1.9.4' } }, start: true })
    expect(created).toMatchObject({ state: 'waiting_approval', approval_required: true, kind: 'deploy', requested_by: 1 })
    expect(created.approval?.decision).toBe('pending')
    expect(created.legs.map((leg) => leg.state)).toEqual(['succeeded', 'pending', 'pending', 'pending'])
  })

  it('refuses a selection that cannot be planned with 422 and the problems', async () => {
    signInAs(adminMe)
    await expect(api('POST', '/api/v1/jobs/1/runs', { environment: 'hetzner-01', selection: {}, start: false })).rejects.toMatchObject({ status: 422 })
    const wire = await fetch('/api/v1/jobs/1/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ environment: 'nowhere', selection: {}, start: false }),
    })
    const body = (await wire.json()) as { error: { problems: { message: string }[] } }
    expect(wire.status).toBe(422)
    expect(body.error.problems.map((problem) => problem.message)).toEqual(['environment "nowhere" is not in the inventory', 'group "backends" needs a version'])
  })

  it('cancels a planned run at once and a running run into cancelling, and refuses to cancel a finished one', async () => {
    signInAs(adminMe)
    expect((await api<RunDetail>('POST', `/api/v1/runs/${runIds.planned}/cancel`)).state).toBe('cancelled')
    const cancelling = await api<RunDetail>('POST', `/api/v1/runs/${runIds.running}/cancel`)
    expect(cancelling).toMatchObject({ state: 'cancelling', cancel_requested: true })
    await expect(api('POST', `/api/v1/runs/${runIds.succeeded}/cancel`)).rejects.toMatchObject({ status: 409 })
  })

  it('settles a run needing reconciliation when its last unknown leg is decided', async () => {
    signInAs(adminMe)
    const first = await api<RunDetail>('POST', `/api/v1/runs/${runIds.needsReconciliation}/legs/10605/reconcile`, { outcome: 'succeeded' })
    expect(first.state).toBe('needs_reconciliation')
    const last = await api<RunDetail>('POST', `/api/v1/runs/${runIds.needsReconciliation}/legs/10606/reconcile`, { outcome: 'failed' })
    expect(last.state).toBe('partially_succeeded')
    expect(last.locks).toEqual([])
    await expect(api('POST', `/api/v1/runs/${runIds.needsReconciliation}/legs/10606/reconcile`, { outcome: 'failed' })).rejects.toMatchObject({ status: 409 })
    await expect(api('POST', `/api/v1/runs/${runIds.running}/legs/10404/check-again`)).rejects.toMatchObject({ status: 409 })
  })

  it('refuses releasing locks while a leg is being checked again, and releases them otherwise', async () => {
    signInAs(adminMe)
    await api('POST', `/api/v1/runs/${runIds.needsReconciliation}/legs/10605/check-again`)
    await expect(api('POST', `/api/v1/admin/runs/${runIds.needsReconciliation}/release-locks`)).rejects.toMatchObject({ status: 409 })
    mockState().runs.find((candidate) => candidate.id === runIds.needsReconciliation)!.legs[4]!.state = 'unknown'
    const released = await api<RunDetail>('POST', `/api/v1/admin/runs/${runIds.needsReconciliation}/release-locks`)
    expect(released).toMatchObject({ state: 'failed', locks: [] })
  })

  it('lets only another person approve, only the plan they saw, and only once', async () => {
    signInAs({ ...grantee('approve'), id: 6, name: 'Yael Barak' })
    await expect(api('POST', `/api/v1/runs/${runIds.waitingApproval}/approve`, { manifest_id: fakeHash(103) })).rejects.toMatchObject({ status: 403 })
    const own = await api<{ approvals: { run_id: number; can_approve: boolean; cannot_approve_reason: string }[] }>('GET', '/api/v1/approvals')
    expect(own.approvals).toEqual([expect.objectContaining({ run_id: 103, can_approve: false, cannot_approve_reason: expect.stringContaining('You started this run') })])

    signInAs(grantee('approve'))
    await expect(api('POST', `/api/v1/runs/${runIds.waitingApproval}/approve`, { manifest_id: 'stale' })).rejects.toMatchObject({
      status: 409,
      message: 'The plan changed. Reload and review it again.',
    })
    const approved = await api<RunDetail>('POST', `/api/v1/runs/${runIds.waitingApproval}/approve`, { manifest_id: fakeHash(103), comment: 'ok' })
    expect(approved).toMatchObject({ state: 'queued', approval: { decision: 'approved', decided_by_name: 'Gal Grantee', comment: 'ok' } })
    await expect(api('POST', `/api/v1/runs/${runIds.waitingApproval}/reject`, {})).rejects.toMatchObject({ status: 409, message: 'This run was already decided.' })
    expect((await api<{ approvals: unknown[] }>('GET', '/api/v1/approvals')).approvals).toEqual([])
  })

  it('creates a restore run that waits for its plan, and refuses to restore a run that changed nothing', async () => {
    signInAs(adminMe)
    const restore = await api<RunDetail>('POST', `/api/v1/runs/${runIds.succeeded}/restore`, {})
    expect(restore).toMatchObject({ kind: 'restore', state: 'planned', restore_of: 109 })
    expect(restore.steps.map((step) => step.name)).toEqual(['restore-config', 'restore-deploy'])
    expect(await api('GET', `/api/v1/runs/${restore.id}/plan`)).toMatchObject({ run_id: restore.id })
    await expect(api('POST', `/api/v1/runs/${runIds.planned}/restore`, {})).rejects.toMatchObject({ status: 409 })
  })

  it('re-runs the failed and skipped services of a run with the versions they had', async () => {
    signInAs(adminMe)
    const rerun = await api<RunDetail>('POST', `/api/v1/runs/${runIds.partiallySucceeded}/rerun-failed`)
    expect(rerun.state).toBe('planned')
    expect(rerun.selection).toEqual({ portal: { version: '2.3.9' } })
    expect([...new Set(rerun.legs.filter((leg) => leg.kind === 'step').map((leg) => leg.service))]).toEqual(['billing'])
    await expect(api('POST', `/api/v1/runs/${runIds.succeeded}/rerun-failed`)).rejects.toMatchObject({ status: 409 })
  })

  it('filters and pages the run list, newest first', async () => {
    const all = await api<{ runs: { id: number }[]; next_before: number | null }>('GET', '/api/v1/runs')
    expect(all.runs.map((r) => r.id)).toEqual([109, 108, 107, 106, 105, 104, 103, 102, 101])
    expect(all.next_before).toBeNull()
    const first = await api<{ runs: { id: number }[]; next_before: number | null }>('GET', '/api/v1/runs?limit=3')
    expect(first.runs.map((r) => r.id)).toEqual([109, 108, 107])
    expect(first.next_before).toBe(107)
    const states = await api<{ runs: { id: number }[] }>('GET', '/api/v1/runs?state=planned&state=running')
    expect(states.runs.map((r) => r.id)).toEqual([104, 102, 101])
    const byJob = await api<{ runs: { id: number }[] }>('GET', '/api/v1/runs?job=2&environment=hetzner-02&q=billing')
    expect(byJob.runs.map((r) => r.id)).toEqual([107])
    await expect(api('GET', '/api/v1/runs?state=bogus')).rejects.toMatchObject({ status: 400 })
  })

  it('serves the log in pages with filters, and refuses it without view_logs', async () => {
    const full = await api<{ lines: { leg_id: number; seq: number }[]; next: string }>('GET', `/api/v1/runs/${runIds.partiallySucceeded}/log?limit=1000`)
    const pageOne = await api<{ lines: unknown[]; next: string }>('GET', `/api/v1/runs/${runIds.partiallySucceeded}/log?limit=4`)
    const pageTwo = await api<{ lines: unknown[] }>('GET', `/api/v1/runs/${runIds.partiallySucceeded}/log?limit=1000&after=${pageOne.next}`)
    expect([...pageOne.lines, ...pageTwo.lines]).toEqual(full.lines)
    const billing = await api<{ lines: { text: string }[] }>('GET', `/api/v1/runs/${runIds.partiallySucceeded}/log?service=billing&level=error`)
    expect(billing.lines.map((l) => l.text)).toEqual([expect.stringContaining('NoSuchKey')])
    await expect(api('GET', `/api/v1/runs/${runIds.partiallySucceeded}/log?after=garbage`)).rejects.toMatchObject({ status: 400 })
    signInAs(viewerMe)
    await expect(api('GET', `/api/v1/runs/${runIds.partiallySucceeded}/log`)).rejects.toMatchObject({ status: 403 })
  })

  it('takes view_logs and run grants of one label only for runs of that label', async () => {
    const devOnly: Me = { ...viewerMe, grants: [{ action: 'view_logs', label: 'dev', environment: '', folder_id: 0 }] }
    signInAs(devOnly)
    await expect(api('GET', `/api/v1/runs/${runIds.partiallySucceeded}/log`)).resolves.toBeDefined() // hetzner-02 is dev
    await expect(api('GET', `/api/v1/runs/${runIds.waitingApproval}/log`)).rejects.toMatchObject({ status: 403 }) // prod
  })

  it('keeps freezes: validates, lists active and ended, and lifts one', async () => {
    signInAs(adminMe)
    const lists = () => api<{ active: { id: number }[]; ended: { id: number }[] }>('GET', '/api/v1/freezes')
    expect(await lists()).toMatchObject({ active: [{ id: 1 }], ended: [{ id: 2 }] })
    await expect(api('POST', '/api/v1/admin/freezes', { label: 'dev', reason: '', expires_at: '2026-11-29T00:00:00Z' })).rejects.toMatchObject({ status: 400 })
    await expect(api('POST', '/api/v1/admin/freezes', { label: 'dev', environment: 'hetzner-01', reason: 'x', expires_at: '2026-11-29T00:00:00Z' })).rejects.toMatchObject({ status: 400 })
    await expect(api('POST', '/api/v1/admin/freezes', { label: 'dev', reason: 'x', expires_at: '2027-03-01T00:00:00Z' })).rejects.toMatchObject({ status: 400 })
    await api('POST', '/api/v1/admin/freezes', { environment: 'hetzner-01', reason: 'Demo day', expires_at: '2026-11-29T00:00:00Z' })
    expect((await lists()).active.map((freeze) => freeze.id)).toEqual([1, 3])
    await api('DELETE', '/api/v1/admin/freezes/1')
    expect(await lists()).toMatchObject({ active: [{ id: 3 }], ended: [{ id: 1 }, { id: 2 }] })
    await expect(api('DELETE', '/api/v1/admin/freezes/1')).rejects.toMatchObject({ status: 409 })
  })

  it('keeps the Slack settings without ever returning the token, and reports a missing channel by code', async () => {
    signInAs(connectionsManagerMe)
    await api('PUT', '/api/v1/admin/slack', { token: 'xoxb-secret-value' })
    const settings = await api<Record<string, unknown>>('GET', '/api/v1/admin/slack')
    expect(settings).toEqual({ token_set: true, set_at: '2026-11-27T12:00:00Z', set_by_name: 'Tamar Golan' })
    expect(JSON.stringify(settings)).not.toContain('xoxb')
    await expect(api('POST', '/api/v1/admin/slack/test', { channel: '#missing' })).resolves.toEqual({ ok: false, code: 'channel_not_found' })
  })

  it('versions: lists each group, and says which service is not in the environment', async () => {
    const versions = await api<{ groups: Record<string, { common: string[]; unavailable: Record<string, string> }> }>('GET', '/api/v1/jobs/3/versions?environment=hetzner-03')
    expect(versions.groups.portal?.unavailable).toEqual({ billing: 'not in this environment' })
    expect(versions.groups.portal?.common).toEqual(['1.9.4', '1.9.3', '1.9.2', '1.9.1'])
    await expect(api('GET', '/api/v1/jobs/3/versions?environment=nowhere')).rejects.toMatchObject({ status: 404 })
  })
})
