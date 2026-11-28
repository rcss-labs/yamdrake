import type { JobDetail } from '../../api/types'
import { Facts, Time, runStatusLabels } from '../../ui'
import { useRuns } from '../runs/queries'
import { jobRunStats } from '../runs/runStats'
import './job-page.css'

// The server returns at most 100 runs per request; a job with more shows an approximate count.
const FACT_RUNS = 100

export function JobFacts({ job }: { job: JobDetail }) {
  const query = useRuns({ job: job.id, limit: FACT_RUNS })
  if (!query.data) {
    const value = query.isError ? 'Not available' : '—'
    return <Facts className="job-facts" items={['Last run', 'Success rate', 'Last success', 'Runs'].map((label) => ({ label, value }))} />
  }
  const { runs, next_before: more } = query.data
  const stats = jobRunStats(runs)
  const { lastRun, lastSuccess } = stats
  return (
    <Facts
      className="job-facts"
      items={[
        {
          label: 'Last run',
          value: lastRun ? (
            <>
              {runStatusLabels[lastRun.state]}, <Time iso={lastRun.started_at ?? lastRun.created_at} />
            </>
          ) : (
            'Never run'
          ),
        },
        {
          label: 'Success rate',
          value:
            stats.successPercent === null
              ? 'No finished runs yet'
              : `${stats.successPercent}% of ${stats.finished} finished ${stats.finished === 1 ? 'run' : 'runs'}`,
        },
        {
          label: 'Last success',
          value: lastSuccess ? (
            <>
              {lastSuccess.environment}, <Time iso={lastSuccess.started_at ?? lastSuccess.created_at} />
            </>
          ) : (
            'None yet'
          ),
        },
        { label: 'Runs', value: more === null ? String(runs.length) : `${runs.length}+` },
      ]}
    />
  )
}
