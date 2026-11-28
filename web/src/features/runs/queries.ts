// The GET queries of the run screens, with fixed keys so a mutation can invalidate exactly what it changed.
// Mutations (start, cancel, approve, ...) are written by the screen that owns the button.
import type { QueryKey } from '@tanstack/react-query'
import { useApiGet } from '../../api/hooks'
import type {
  ApprovalsResponse,
  FreezesResponse,
  JobVersions,
  Plan,
  RunsFilter,
  RunsResponse,
  SlackSettings,
} from '../../api/types'

export const runsKey = ['runs'] as const
export const runsListKey = (filter: RunsFilter): QueryKey => ['runs', 'list', filter]
export const runPlanKey = (runId: number | string): QueryKey => ['runs', 'plan', String(runId)]
export const jobVersionsKey = (jobId: number | string, environment: string): QueryKey => ['job-versions', String(jobId), environment]
export const approvalsKey = ['approvals'] as const
export const freezesKey = ['freezes'] as const
export const slackKey = ['admin', 'slack'] as const

/** "/api/v1/runs?job=3&state=failed&state=cancelled": the filters that are set, in a fixed order. */
export function runsPath(filter: RunsFilter = {}): string {
  const params = new URLSearchParams()
  if (filter.job !== undefined) params.set('job', String(filter.job))
  if (filter.environment) params.set('environment', filter.environment)
  if (filter.q) params.set('q', filter.q)
  for (const state of filter.state ?? []) params.append('state', state)
  if (filter.before !== undefined) params.set('before', String(filter.before))
  if (filter.limit !== undefined) params.set('limit', String(filter.limit))
  const query = params.toString()
  return query === '' ? '/api/v1/runs' : `/api/v1/runs?${query}`
}

interface QueryOptions {
  enabled?: boolean
  /** Milliseconds between refreshes; leave out to refresh only when something invalidates the query. */
  refetchInterval?: number
}

/** GET /api/v1/runs with the filters (the newest runs first; `next_before` pages back). */
export function useRuns(filter: RunsFilter = {}, options: QueryOptions = {}) {
  return useApiGet<RunsResponse>(runsListKey(filter), runsPath(filter), options)
}

/** GET /api/v1/runs/{id}/plan: the redacted manifest. 404 until the run has been planned. */
export function useRunPlan(runId: number | string, options: QueryOptions = {}) {
  return useApiGet<Plan>(runPlanKey(runId), `/api/v1/runs/${runId}/plan`, options)
}

/** A map Go sent as null (a nil map) is empty; the client's list rule has already turned some of those into []. */
const mapOrEmpty = <T>(value: Record<string, T> | null | undefined | T[]): Record<string, T> =>
  value === null || value === undefined || Array.isArray(value) ? {} : value

/** The versions response with every null map or list made empty, so the screens never test `in` on null. */
function normaliseVersions(data: JobVersions): JobVersions {
  const groups = Object.fromEntries(
    Object.entries(mapOrEmpty(data.groups)).map(([id, group]) => [
      id,
      { common: group.common ?? [], services: mapOrEmpty(group.services), unavailable: mapOrEmpty(group.unavailable) },
    ]),
  )
  return { groups }
}

/** GET /api/v1/jobs/{id}/versions?environment=: what each group's services can be deployed at. Waits for an environment. */
export function useJobVersions(jobId: number | string, environment: string, options: QueryOptions = {}) {
  return useApiGet<JobVersions>(
    jobVersionsKey(jobId, environment),
    `/api/v1/jobs/${jobId}/versions?environment=${encodeURIComponent(environment)}`,
    { ...options, enabled: environment !== '' && (options.enabled ?? true), select: normaliseVersions },
  )
}

/** GET /api/v1/approvals: runs waiting for a decision, with whether the signed-in person may make it. */
export function useApprovals(options: QueryOptions = {}) {
  return useApiGet<ApprovalsResponse>(approvalsKey, '/api/v1/approvals', options)
}

/** GET /api/v1/freezes: the active freezes and the recently ended ones. */
export function useFreezes(options: QueryOptions = {}) {
  return useApiGet<FreezesResponse>(freezesKey, '/api/v1/freezes', options)
}

/** GET /api/v1/admin/slack (manage_connections): pass `enabled: false` for anyone without it. */
export function useSlack(options: QueryOptions = {}) {
  return useApiGet<SlackSettings>(slackKey, '/api/v1/admin/slack', options)
}
