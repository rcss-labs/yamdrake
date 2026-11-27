import { useInfiniteQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import type { AuditResponse } from '../../api/types'

const PAGE_SIZE = 25

// Pages are requested with before=<id of the last entry shown>; the server says when there are no older ones.
export function useAuditLog() {
  return useInfiniteQuery<AuditResponse, ApiError, { entries: AuditResponse['entries'] }, string[], number | null>({
    queryKey: ['admin', 'audit', 'pages'],
    initialPageParam: null,
    queryFn: ({ pageParam }) =>
      api<AuditResponse>('GET', `/api/v1/admin/audit?limit=${PAGE_SIZE}${pageParam === null ? '' : `&before=${pageParam}`}`),
    getNextPageParam: (last) => last.next_before ?? undefined,
    select: (data) => ({ entries: data.pages.flatMap((page) => page.entries) }),
  })
}
