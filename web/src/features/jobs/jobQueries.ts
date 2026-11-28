import { useQueryClient } from '@tanstack/react-query'
import { useApiGet, useApiMutation } from '../../api/hooks'
import type { JobDetail } from '../../api/types'

/** One job with its last good definition and the problems of the newest commit. */
export function useJob(id: string) {
  return useApiGet<JobDetail>(['jobs', id], `/api/v1/jobs/${encodeURIComponent(id)}`)
}

/** Reads the job file from GitHub again; on success the job is shown from the answer and refetched. */
export function useSyncJob(id: string) {
  const queryClient = useQueryClient()
  return useApiMutation<JobDetail>('POST', `/api/v1/jobs/${encodeURIComponent(id)}/sync`, {
    onSuccess: (job) => {
      queryClient.setQueryData(['jobs', id], job)
    },
    // Marks the list and this job stale (a key prefix match), so the page refreshes from the server.
    invalidate: [['jobs']],
  })
}
