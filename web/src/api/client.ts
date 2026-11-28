import { ApiError } from './error'
import type { ErrorBody } from './types'

export { ApiError }

// Where the rest of the app learns the response headers it needs (sign-in shows Retry-After).
declare module './error' {
  interface ApiError {
    /** Set when the response had a numeric Retry-After header. */
    retryAfterSeconds?: number
  }
}

export const REQUEST_TIMEOUT_MS = 15_000

type UnauthorizedListener = () => void
const unauthorizedListeners = new Set<UnauthorizedListener>()

/**
 * Subscribes to "a request was answered with 401": the session has ended.
 * The sign-in routes are excluded, where 401 only means the credentials were wrong.
 * Returns the function that unsubscribes.
 */
export function onUnauthorized(listener: UnauthorizedListener): () => void {
  unauthorizedListeners.add(listener)
  return () => {
    unauthorizedListeners.delete(listener)
  }
}

// Every field the API documents as a list. Go sends null for a list nobody appended to, and the screens
// map and search these lists, so a null here would blank the whole page. Other nulls (for example
// last_used_at) are real values and stay.
const LIST_FIELDS: ReadonlySet<string> = new Set([
  'accounts',
  'approval_labels',
  'connections',
  'entries',
  'env_files',
  'environments',
  'folders',
  'grants',
  'group_ids',
  'groups',
  'jobs',
  'labels',
  'mappings',
  'needs',
  'problems',
  'services',
  'steps',
  'tokens',
  'tomls',
  'users',
])

/** Replaces a null in any list field, at any depth, with []. Pure; returns new objects only where needed. */
export function nullListsToEmpty(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(nullListsToEmpty)
  }
  if (typeof value === 'object' && value !== null) {
    return Object.fromEntries(
      Object.entries(value).map(([key, inner]) => [key, inner === null && LIST_FIELDS.has(key) ? [] : nullListsToEmpty(inner)]),
    )
  }
  return value
}

function isErrorBody(value: unknown): value is ErrorBody {
  if (typeof value !== 'object' || value === null || !('error' in value)) {
    return false
  }
  const inner = (value as { error: unknown }).error
  return (
    typeof inner === 'object' &&
    inner !== null &&
    typeof (inner as { code?: unknown }).code === 'string' &&
    typeof (inner as { message?: unknown }).message === 'string'
  )
}

async function toApiError(response: Response): Promise<ApiError> {
  let code = `http_${response.status}`
  let message = `The server answered with status ${response.status}`
  try {
    const parsed: unknown = JSON.parse(await response.text())
    if (isErrorBody(parsed)) {
      code = parsed.error.code
      message = parsed.error.message
    }
  } catch {
    // Not JSON (for example a gateway page): keep the fallback code and message.
  }
  const error = new ApiError(response.status, code, message)
  const retryAfter = Number(response.headers.get('Retry-After'))
  if (response.headers.has('Retry-After') && Number.isFinite(retryAfter) && retryAfter >= 0) {
    error.retryAfterSeconds = retryAfter
  }
  return error
}

/**
 * Calls the API: same-origin credentials, JSON in and out, a 15 second timeout.
 * Resolves to undefined for 204. Rejects with ApiError; a request that never completed
 * (network failure or timeout) has status 0 and code "network".
 */
export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const controller = new AbortController()
  let timedOut = false
  const timer = setTimeout(() => {
    timedOut = true
    controller.abort()
  }, REQUEST_TIMEOUT_MS)

  let response: Response
  try {
    response = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: controller.signal,
    })
  } catch {
    clearTimeout(timer)
    throw new ApiError(
      0,
      'network',
      timedOut
        ? 'The server did not answer in time. Try again.'
        : 'Could not reach the server. Check your connection and try again.',
    )
  }

  try {
    if (!response.ok) {
      const error = await toApiError(response)
      if (response.status === 401 && !path.startsWith('/api/v1/auth/')) {
        unauthorizedListeners.forEach((listener) => listener())
      }
      throw error
    }
    if (response.status === 204) {
      return undefined as T
    }
    return nullListsToEmpty(await response.json()) as T
  } catch (caught) {
    if (caught instanceof ApiError) {
      throw caught
    }
    if (timedOut) {
      throw new ApiError(0, 'network', 'The server did not answer in time. Try again.')
    }
    throw new ApiError(0, 'network', 'Could not read the answer from the server. Try again.')
  } finally {
    clearTimeout(timer)
  }
}

/** Plain words for an error that a screen shows next to what failed. */
export function errorMessage(error: unknown): string {
  return error instanceof ApiError ? error.message : 'Something went wrong. Try again.'
}
