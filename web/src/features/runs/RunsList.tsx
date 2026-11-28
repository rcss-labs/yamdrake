import { keepPreviousData, useInfiniteQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import type { RunsFilter, RunsResponse } from '../../api/types'
import { useInventory } from '../inventory/queries'
import { Button, Card, EmptyState, ErrorState, Loading, SearchBox, Segmented, Select } from '../../ui'
import { runsKey, runsPath } from './queries'
import { statesBySegment, statusOptions, useRunFilters } from './runFilters'
import { RunsTable } from './RunsTable'
import './runs.css'

const SEARCH_DELAY_MS = 300

/** The value, a little after it stopped changing. The first value is used at once. */
function useDebounced(value: string): string {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), SEARCH_DELAY_MS)
    return () => clearTimeout(timer)
  }, [value])
  return debounced
}

export interface RunsListProps {
  /** Only this job's runs (its source id); leave out for every job. */
  job?: number
  /** Offer the search box (job, label, environment, manifest or run id). */
  search?: boolean
  /** Shown when there is no run at all, with no filter set. */
  noRunsText: string
}

/** Filters, the table and "Load older" shared by a job's History tab and the Runs page. */
export function RunsList({ job, search = false, noRunsText }: RunsListProps) {
  const filters = useRunFilters()
  const inventory = useInventory()
  const environments = inventory.data?.environments ?? []
  // The box and the address follow every key; the request waits for a pause in typing.
  const query = useDebounced(filters.q.trim())
  const filter: RunsFilter = {
    ...(job === undefined ? {} : { job }),
    ...(filters.environment === '' ? {} : { environment: filters.environment }),
    ...(query === '' ? {} : { q: query }),
    ...(filters.status === 'all' ? {} : { state: statesBySegment[filters.status] }),
  }
  const filtered = filters.status !== 'all' || filters.environment !== '' || query !== ''

  return (
    <div className="runs-list">
      <div className="runs-toolbar">
        {search && <SearchBox label="Job, label, environment, manifest or run id" value={filters.q} onChange={filters.setQ} />}
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
      <RunsPages filter={filter} showJob={job === undefined} noRunsText={filtered ? 'No runs match these filters' : noRunsText} />
    </div>
  )
}

function RunsPages({ filter, showJob, noRunsText }: { filter: RunsFilter; showJob: boolean; noRunsText: string }) {
  // One accumulating query per filter: a new filter starts again at the newest page, and a refetch
  // re-reads every loaded page in order from fresh cursors, so no run is lost or repeated between pages.
  const query = useInfiniteQuery<RunsResponse, ApiError, { pages: RunsResponse[] }, unknown[], number | undefined>({
    queryKey: [...runsKey, 'list', filter, 'pages'],
    initialPageParam: undefined,
    queryFn: ({ pageParam }) => api<RunsResponse>('GET', runsPath(pageParam === undefined ? filter : { ...filter, before: pageParam })),
    getNextPageParam: (last) => last.next_before ?? undefined,
    placeholderData: keepPreviousData,
  })

  if (query.isError && !query.data) {
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />
  }
  if (!query.data) return <Loading />

  const runs = query.data.pages.flatMap((page) => page.runs)
  return (
    <Card flush>
      {runs.length === 0 ? (
        <EmptyState title={noRunsText} />
      ) : (
        <>
          <RunsTable runs={runs} showJob={showJob} />
          {query.isFetchNextPageError && (
            <div className="runs-more" role="alert">
              <span>{`Older runs could not be loaded. ${query.error.message}`}</span>
            </div>
          )}
          {query.hasNextPage && (
            <div className="runs-more">
              <Button disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
                Load older
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  )
}
