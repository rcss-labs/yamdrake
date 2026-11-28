import type { RunSummary } from '../../api/types'

// Placeholder: Task 3 of Plan 8b fills this in.
export function RunsTable({ runs, showJob }: { runs: RunSummary[]; showJob: boolean }) {
  return <p>{`${runs.length} runs${showJob ? ' with the job column' : ''}`}</p>
}
