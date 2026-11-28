import type { JobDetail } from '../../api/types'

// Placeholder: Task 2 of Plan 8b fills this in.
export function RunTab({ job }: { job: JobDetail }) {
  return <p>Run tab of {job.job_id || job.path}</p>
}
