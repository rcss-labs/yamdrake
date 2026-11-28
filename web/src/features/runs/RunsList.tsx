import { useQueries } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import type { RunsFilter, RunsResponse } from '../../api/types'
import { useInventory } from '../inventory/queries'
import { Button, Card, EmptyState, ErrorState, Loading, SearchBox, Segmented, Select } from '../../ui'
import { runsListKey, runsPath } from './queries'
import { statesBySegment, statusOptions, useRunFilters } from './runFilters'
import { RunsTable } from './RunsTable'
import './runs.css'

export interface RunsListProps {
  /** Only this job's runs (its source id); leave out for every job. */
  job?: number
  /** Offer the search box (job, service or person). */
  search?: boolean
  /** Shown when there is no run at all, with no filter set. */
  noRunsText: string
}

/** Filters, the table and "Load older" shared by a job's History tab and the Runs page. */
export function RunsList({ job, search = false, noRunsText }: RunsListProps) {
  const filters = useRunFilters()
  const inventory = useInventory()
  const environments = inventory.data?.environments ?? []
  const filter: RunsFilter = {
    ...(job === undefined ? {} : { job }),
    ...(filters.environment === '' ? {} : { environment: filters.environment }),
    ...(filters.q.trim() === '' ? {} : { q: filters.q.trim() }),
    ...(filters.status === 'all' ? {} : { state: statesBySegment[filters.status] }),
  }
  const filtered = filters.status !== 'all' || filters.environment !== '' || filters.q.trim() !== ''

  return (
    <div className="runs-list">
      <div className="runs-toolbar">
        {search && <SearchBox label="Job, service or person" value={filters.q} onChange={filters.setQ} />}
        <Segmented label="Filter by status" options={statusOptions} value={filters.status} onChange={filters.setStatus} />
        <Select aria-label="Filter by environment" value={filters.environment} onChange={(event) => filters.setEnvironment(event.target.value)}>
          <option value="">All environments</option>
          {filters.environment !== '' && !environments.some((environment) => environment.name === filters.environment) && (
            <option value={filters.environment}>{filters.environment}</option>
          )}
          {environments.map((environment) => (
            <option key={environment.name} value={environment.name}>
              {`${environment.name} (${environment.label})`}
            </option>
          ))}
        </Select>
      </div>
      {/* Keyed by the filter so a changed filter starts again from the newest page. */}
      <RunsPages key={JSON.stringify(filter)} filter={filter} showJob={job === undefined} noRunsText={filtered ? 'No runs match these filters' : noRunsText} />
    </div>
  )
}

function RunsPages({ filter, showJob, noRunsText }: { filter: RunsFilter; showJob: boolean; noRunsText: string }) {
  // Each "Load older" adds the `before` of the last page loaded.
  const [cursors, setCursors] = useState<number[]>([])
  const pages = useQueries({
    queries: [undefined, ...cursors].map((before) => {
      const pageFilter: RunsFilter = before === undefined ? filter : { ...filter, before }
      return {
        queryKey: runsListKey(pageFilter),
        queryFn: () => api<RunsResponse>('GET', runsPath(pageFilter)),
      }
    }),
  })

  const failed = pages.find((page) => page.isError)
  if (failed) {
    return <ErrorState error={failed.error as ApiError} onRetry={() => void failed.refetch()} />
  }
  const first = pages[0]
  if (!first || first.isPending) return <Loading />

  const runs = pages.flatMap((page) => page.data?.runs ?? [])
  const last = pages[pages.length - 1]
  const nextBefore = last?.data?.next_before ?? null

  return (
    <Card flush>
      {runs.length === 0 ? (
        <EmptyState title={noRunsText} />
      ) : (
        <>
          <RunsTable runs={runs} showJob={showJob} />
          {last?.isPending && <Loading />}
          {nextBefore !== null && (
            <div className="runs-more">
              <Button onClick={() => setCursors([...cursors, nextBefore])}>Load older</Button>
            </div>
          )}
        </>
      )}
    </Card>
  )
}
