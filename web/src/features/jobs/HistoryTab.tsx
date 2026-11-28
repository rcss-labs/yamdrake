import type { JobDetail } from '../../api/types'

// Placeholder: Task 3 of Plan 8b fills this in.
export function HistoryTab({ job }: { job: JobDetail }) {
  return <p>History tab of {job.job_id || job.path}</p>
}
