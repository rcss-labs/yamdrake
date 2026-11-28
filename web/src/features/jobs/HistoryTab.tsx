import type { JobDetail } from '../../api/types'
import { RunsList } from '../runs/RunsList'

export function HistoryTab({ job }: { job: JobDetail }) {
  return <RunsList job={job.id} noRunsText="This job has not run yet" />
}
