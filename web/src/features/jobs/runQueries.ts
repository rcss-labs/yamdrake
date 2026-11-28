import { useApiMutation } from '../../api/hooks'
import type { CreateRunRequest, RunDetail } from '../../api/types'
import { runsKey } from '../runs/queries'

/** POST /api/v1/jobs/{id}/runs. The Run tab always sends start: false, so the run waits as planned. A 422 carries `error.problems`. */
export function useCreateRun(jobId: number) {
  return useApiMutation<RunDetail, CreateRunRequest>('POST', `/api/v1/jobs/${jobId}/runs`, { invalidate: [runsKey] })
}
