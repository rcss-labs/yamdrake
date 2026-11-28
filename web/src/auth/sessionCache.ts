import type { QueryClient } from '@tanstack/react-query'
import type { Me } from '../api/types'

/** The cached answer of GET /api/v1/me: the person, or null when nobody is signed in. */
export const meQueryKey = ['session', 'me'] as const

function forgetEverythingButTheSession(queryClient: QueryClient) {
  queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== meQueryKey[0] })
}

/** The session is over (sign-out or a 401): nothing the previous person could see stays cached. */
export function endSession(queryClient: QueryClient): void {
  forgetEverythingButTheSession(queryClient)
  queryClient.setQueryData(meQueryKey, null)
}

/** Someone signed in: start from an empty cache and remember who it is. */
export function startSession(queryClient: QueryClient, me: Me): void {
  forgetEverythingButTheSession(queryClient)
  queryClient.setQueryData(meQueryKey, me)
}

/**
 * What the person may do can change when users, groups, grants or mappings change (including their own),
 * so the cached answer of GET /api/v1/me is refreshed after any such change.
 */
export function refreshMe(queryClient: QueryClient): Promise<void> {
  return queryClient.invalidateQueries({ queryKey: meQueryKey })
}
