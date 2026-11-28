import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import { runIds } from '../../test/fixtures'
import { requests, signInAs, viewerMe } from '../../test/server'
import { runsPath, useApprovals, useFreezes, useJobVersions, useRunPlan, useRuns, useSlack } from './queries'

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

describe('runsPath', () => {
  it('writes the filters that are set, repeating state', () => {
    expect(runsPath()).toBe('/api/v1/runs')
    expect(runsPath({ job: 3, environment: 'prod-01', q: 'billing', state: ['failed', 'cancelled'], before: 50, limit: 25 })).toBe(
      '/api/v1/runs?job=3&environment=prod-01&q=billing&state=failed&state=cancelled&before=50&limit=25',
    )
  })
})

describe('the run queries', () => {
  it('lists runs with the filters and the paging cursor', async () => {
    const { result } = renderHook(() => useRuns({ state: ['planned'], limit: 1 }), { wrapper })
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(result.current.data?.runs.map((run) => run.id)).toEqual([102])
    expect(result.current.data?.next_before).toBe(102)
    expect(requests.at(-1)?.search).toBe('?state=planned&limit=1')
  })

  it('reads the plan of a run, and gives the 404 of a run without one', async () => {
    const { result } = renderHook(() => useRunPlan(runIds.planned), { wrapper })
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(result.current.data?.services.map((service) => service.name)).toEqual(['backend-api', 'billing'])
    const missing = renderHook(() => useRunPlan(9999), { wrapper })
    await waitFor(() => expect(missing.result.current.error?.status).toBe(404))
  })

  it('reads the versions of a job in one environment, and waits for an environment', async () => {
    const waiting = renderHook(() => useJobVersions(1, ''), { wrapper })
    expect(waiting.result.current.fetchStatus).toBe('idle')
    const { result } = renderHook(() => useJobVersions(1, 'hetzner-01'), { wrapper })
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(result.current.data?.groups.backends?.common).toContain('1.9.4')
  })

  it('reads approvals, freezes and the Slack settings', async () => {
    const approvals = renderHook(() => useApprovals(), { wrapper })
    const freezes = renderHook(() => useFreezes(), { wrapper })
    const slack = renderHook(() => useSlack(), { wrapper })
    await waitFor(() => expect(approvals.result.current.data?.approvals).toHaveLength(1))
    await waitFor(() => expect(freezes.result.current.data?.active).toHaveLength(1))
    await waitFor(() => expect(slack.result.current.data?.token_set).toBe(true))
  })

  it('gives the 403 of a person without manage_connections for Slack', async () => {
    signInAs(viewerMe)
    const { result } = renderHook(() => useSlack(), { wrapper })
    await waitFor(() => expect(result.current.error?.status).toBe(403))
  })
})
