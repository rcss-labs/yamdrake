import { QueryClient } from '@tanstack/react-query'
import { ApiError } from './error'

/** Reads may be tried again when the server or network failed; a refusal (4xx) will not change. */
function retryRead(failureCount: number, error: unknown): boolean {
  const transient = error instanceof ApiError && (error.status === 0 || error.status >= 500)
  return transient && failureCount < 2
}

/** The query client of the app. Writes are never retried, so nothing is submitted twice behind a person's back. */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: retryRead, refetchOnWindowFocus: false },
      mutations: { retry: false },
    },
  })
}
