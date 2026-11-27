import { useMutation } from '@tanstack/react-query'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import { useApiGet, useApiMutation } from '../../api/hooks'
import type { AdminUser, CreateUserRequest, GroupsResponse, UpdateUserRequest, UsersResponse } from '../../api/types'

export const usersKey = ['admin', 'users']
export const groupsKey = ['admin', 'groups']

export function useUsers() {
  return useApiGet<UsersResponse>(usersKey, '/api/v1/admin/users')
}

/** Only fetched while the group editor is open. */
export function useGroups(enabled: boolean) {
  return useApiGet<GroupsResponse>(groupsKey, '/api/v1/admin/groups', { enabled })
}

export function useCreateUser() {
  return useApiMutation<AdminUser, CreateUserRequest>('POST', '/api/v1/admin/users', { invalidate: [usersKey, groupsKey] })
}

export type UserChange = UpdateUserRequest & { id: number }

/** PATCH one user; the id goes into the path and is not sent in the body. */
export function useUpdateUser() {
  const queryClient = useQueryClient()
  return useMutation<AdminUser, ApiError, UserChange>({
    mutationFn: ({ id, ...change }) => api<AdminUser>('PATCH', `/api/v1/admin/users/${id}`, change),
    onSuccess: async () => {
      await Promise.all([usersKey, groupsKey].map((queryKey) => queryClient.invalidateQueries({ queryKey })))
    },
  })
}
