import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import { useApiGet, useApiMutation } from '../../api/hooks'
import type {
  CreateFolderRequest,
  Folder,
  FoldersResponse,
  JobDetail,
  JobSummary,
  JobsResponse,
  RegisterJobRequest,
} from '../../api/types'

export const jobsKey = ['jobs']
export const foldersKey = ['folders']

export function useJobs() {
  return useApiGet<JobsResponse>(jobsKey, '/api/v1/jobs')
}

export function useFolders() {
  return useApiGet<FoldersResponse>(foldersKey, '/api/v1/folders')
}

export function useRegisterJob() {
  return useApiMutation<JobDetail, RegisterJobRequest>('POST', '/api/v1/jobs', { invalidate: [jobsKey] })
}

export function useCreateFolder() {
  return useApiMutation<Folder, CreateFolderRequest>('POST', '/api/v1/folders', { invalidate: [foldersKey] })
}

interface MoveJobVariables {
  job: JobSummary
  folder: Folder
}

/**
 * Moves a job to another folder. The list shows the move at once; if the server refuses, the job
 * goes back to the folder it was in and the caller shows why.
 */
export function useMoveJob() {
  const queryClient = useQueryClient()
  const putJobInFolder = (jobId: number, folder: Pick<Folder, 'id' | 'name'>) => {
    queryClient.setQueryData<JobsResponse>(jobsKey, (current) =>
      current && {
        ...current,
        jobs: current.jobs.map((job) => (job.id === jobId ? { ...job, folder_id: folder.id, folder: folder.name } : job)),
      },
    )
  }
  return useMutation<unknown, ApiError, MoveJobVariables, { previous: Pick<Folder, 'id' | 'name'> }>({
    mutationFn: ({ job, folder }) => api('PATCH', `/api/v1/jobs/${job.id}`, { folder_id: folder.id }),
    onMutate: async ({ job, folder }) => {
      await queryClient.cancelQueries({ queryKey: jobsKey })
      putJobInFolder(job.id, folder)
      return { previous: { id: job.folder_id, name: job.folder } }
    },
    onError: (_error, { job }, context) => {
      if (context) {
        putJobInFolder(job.id, context.previous)
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: jobsKey }),
  })
}
