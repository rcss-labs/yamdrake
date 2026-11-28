import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import type { RunDetail } from '../../api/types'
import { runsKey } from './queries'

export type ReconcileOutcome = 'succeeded' | 'failed'

interface LegAction {
  legId: number
  outcome?: ReconcileOutcome
}

// Every answer is the run's new detail. The page learns of the new state from its event stream, so these only
// refresh the lists that show the run.
function useRunAction<TInput = void>(call: (input: TInput) => Promise<RunDetail>) {
  const queryClient = useQueryClient()
  return useMutation<RunDetail, ApiError, TInput>({
    mutationFn: call,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: runsKey }),
  })
}

export function useReconcileLeg(runId: number) {
  return useRunAction(({ legId, outcome }: LegAction) =>
    api<RunDetail>('POST', `/api/v1/runs/${runId}/legs/${legId}/reconcile`, { outcome }),
  )
}

export function useCheckLegAgain(runId: number) {
  return useRunAction(({ legId }: LegAction) => api<RunDetail>('POST', `/api/v1/runs/${runId}/legs/${legId}/check-again`))
}

export function useReleaseLocks(runId: number) {
  return useRunAction(() => api<RunDetail>('POST', `/api/v1/admin/runs/${runId}/release-locks`))
}
