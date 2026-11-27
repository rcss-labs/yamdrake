// Hooks shared by every feature. Feature-specific queries and mutations live in features/<feature>/queries.ts.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { QueryKey, UseMutationOptions, UseQueryOptions } from '@tanstack/react-query'
import { useCallback } from 'react'
import { can } from '../auth/permissions'
import type { Target } from '../auth/permissions'
import { useMe } from '../auth/session'
import { api } from './client'
import type { ApiError } from './error'
import type { Action } from './types'

/** Returns `can` already bound to the signed-in person: `const can = useCan(); can('manage_jobs', { folderId })`. */
export function useCan(): (action: Action, target?: Target) => boolean {
  const me = useMe()
  return useCallback((action, target) => can(me, action, target), [me])
}

/** A GET of one API path as a query. Pass `enabled: false` to wait, for example while a person has no permission. */
export function useApiGet<T>(
  queryKey: QueryKey,
  path: string,
  options: Omit<UseQueryOptions<T, ApiError>, 'queryKey' | 'queryFn'> = {},
) {
  return useQuery<T, ApiError>({ queryKey, queryFn: () => api<T>('GET', path), ...options })
}

interface ApiMutationOptions<TResult, TBody> extends Omit<UseMutationOptions<TResult, ApiError, TBody>, 'mutationFn'> {
  /** Queries to mark stale once the change succeeded. */
  invalidate?: QueryKey[]
}

/**
 * A write to the API. `path` may depend on the body (for example the id of the thing changed).
 * Writes are never retried automatically.
 */
export function useApiMutation<TResult = void, TBody = void>(
  method: 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string | ((body: TBody) => string),
  { invalidate = [], onSuccess, ...options }: ApiMutationOptions<TResult, TBody> = {},
) {
  const queryClient = useQueryClient()
  return useMutation<TResult, ApiError, TBody>({
    ...options,
    mutationFn: (body) => api<TResult>(method, typeof path === 'function' ? path(body) : path, body === undefined ? undefined : body),
    onSuccess: async (result, body, onMutateResult, context) => {
      await Promise.all(invalidate.map((queryKey) => queryClient.invalidateQueries({ queryKey })))
      await onSuccess?.(result, body, onMutateResult, context)
    },
  })
}
