import { approvalsKey, runsKey } from '../runs/queries'
import { useApiMutation } from '../../api/hooks'
import type { RunDetail } from '../../api/types'

/** Rejects a waiting run. The run page's own approve and reject buttons belong to the run screen. */
export function useRejectRun() {
  return useApiMutation<RunDetail, { runId: number }>('POST', ({ runId }) => `/api/v1/runs/${runId}/reject`, {
    invalidate: [approvalsKey, runsKey],
  })
}
