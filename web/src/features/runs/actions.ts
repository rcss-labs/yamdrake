// The writes of the run page. Each returns the run as the server now has it (a restore or re-run: the new run).
import type { ApproveRequest, CreateRunRequest, RestoreRequest, RunDetail, StartRunRequest } from '../../api/types'
import { useApiMutation } from '../../api/hooks'
import { approvalsKey, runsKey } from './queries'

const invalidate = [runsKey, approvalsKey]
const runPath = (runId: number, action: string) => `/api/v1/runs/${runId}/${action}`

export const useStartRun = (runId: number) => useApiMutation<RunDetail, StartRunRequest>('POST', runPath(runId, 'start'), { invalidate })
/** Also what Discard calls. */
export const useCancelRun = (runId: number) => useApiMutation<RunDetail, void>('POST', runPath(runId, 'cancel'), { invalidate })
export const useApproveRun = (runId: number) => useApiMutation<RunDetail, ApproveRequest>('POST', runPath(runId, 'approve'), { invalidate })
export const useRejectRun = (runId: number) => useApiMutation<RunDetail, { comment?: string }>('POST', runPath(runId, 'reject'), { invalidate })
export const useRerunFailed = (runId: number) => useApiMutation<RunDetail, void>('POST', runPath(runId, 'rerun-failed'), { invalidate })
export const useRestoreRun = (runId: number) => useApiMutation<RunDetail, RestoreRequest>('POST', runPath(runId, 'restore'), { invalidate })
export const usePlanAgain = (jobSourceId: number) =>
  useApiMutation<RunDetail, CreateRunRequest>('POST', `/api/v1/jobs/${jobSourceId}/runs`, { invalidate })
