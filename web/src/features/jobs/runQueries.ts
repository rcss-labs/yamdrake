import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '../../api/error'
import type { CreateRunRequest, Problem, RunDetail } from '../../api/types'
import { runsKey } from '../runs/queries'

/** An ApiError that also carries the problems of a 422 answer (the shared client keeps only the message). */
export class SelectionError extends ApiError {
  problems: Problem[]

  constructor(error: ApiError, problems: Problem[]) {
    super(error.status, error.code, error.message)
    this.problems = problems
  }
}

async function postRun(jobId: number, request: CreateRunRequest): Promise<RunDetail> {
  let response: Response
  try {
    response = await fetch(`/api/v1/jobs/${jobId}/runs`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(request),
    })
  } catch {
    throw new ApiError(0, 'network', 'Could not reach the server. Check your connection and try again.')
  }
  if (response.ok) {
    return (await response.json()) as RunDetail
  }
  let error = new ApiError(response.status, `http_${response.status}`, `The server answered with status ${response.status}`)
  let problems: Problem[] = []
  try {
    const body = (await response.json()) as { error?: { code?: string; message?: string; problems?: Problem[] | null } }
    if (body.error?.code && body.error.message) {
      error = new ApiError(response.status, body.error.code, body.error.message)
    }
    problems = body.error?.problems ?? []
  } catch {
    // Not JSON: keep the fallback message.
  }
  throw new SelectionError(error, problems)
}

/** POST /api/v1/jobs/{id}/runs. The Run tab always sends start: false, so the run waits as planned. */
export function useCreateRun(jobId: number) {
  const queryClient = useQueryClient()
  return useMutation<RunDetail, ApiError, CreateRunRequest>({
    mutationFn: (request) => postRun(jobId, request),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: runsKey }),
  })
}
