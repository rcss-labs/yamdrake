import type { RunSummary } from '../../api/types'

// Finished deploys. Cancelled, rejected, expired and invalidated runs never ran to a result, so they are left out.
const finishedStates = new Set(['succeeded', 'partially_succeeded', 'failed'])

export interface JobRunStats {
  lastRun: RunSummary | undefined
  lastSuccess: RunSummary | undefined
  finished: number
  succeeded: number
  /** Whole percent of finished runs that succeeded; null when none finished. A partly succeeded run is not a success. */
  successPercent: number | null
}

/** Numbers for a job's facts row from its runs, newest first. */
export function jobRunStats(runs: RunSummary[]): JobRunStats {
  const finished = runs.filter((run) => finishedStates.has(run.state))
  const succeeded = finished.filter((run) => run.state === 'succeeded')
  return {
    lastRun: runs[0],
    lastSuccess: succeeded[0],
    finished: finished.length,
    succeeded: succeeded.length,
    successPercent: finished.length === 0 ? null : Math.round((succeeded.length / finished.length) * 100),
  }
}
