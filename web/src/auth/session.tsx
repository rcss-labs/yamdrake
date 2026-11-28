import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createContext, useCallback, useContext, useEffect } from 'react'
import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { ApiError, api, errorMessage, onUnauthorized } from '../api/client'
import type { Me } from '../api/types'
import { safeNext } from './next'
import { endSession, meQueryKey } from './sessionCache'
import { SignIn } from './SignIn'
import './session.css'

const SIGN_IN_PATH = '/sign-in'

const MeContext = createContext<Me | null>(null)

async function loadMe(): Promise<Me | null> {
  try {
    return await api<Me>('GET', '/api/v1/me')
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      return null
    }
    throw error
  }
}

/**
 * Loads the signed-in person once and shows only what they may see: a quiet loading line, a readable
 * error with a retry, the sign-in page, or the children. Any 401 from any request ends the session.
 */
export function SessionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const location = useLocation()
  const session = useQuery({
    queryKey: meQueryKey,
    queryFn: loadMe,
    staleTime: Infinity,
    gcTime: Infinity,
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })

  useEffect(() => onUnauthorized(() => endSession(queryClient)), [queryClient])

  if (session.isPending) {
    return (
      <p className="session-note" role="status">
        Loading your session
      </p>
    )
  }
  if (session.isError) {
    return (
      <div className="session-note session-error" role="alert">
        <p>We could not load your session. {errorMessage(session.error)}</p>
        <button type="button" className="session-retry" disabled={session.isFetching} onClick={() => void session.refetch()}>
          Try again
        </button>
      </div>
    )
  }

  const onSignInPage = location.pathname === SIGN_IN_PATH
  if (session.data === null) {
    if (onSignInPage) {
      return <SignIn />
    }
    const here = location.pathname + location.search
    const next = here === '/' ? '' : `?next=${encodeURIComponent(here)}`
    return <Navigate replace to={`${SIGN_IN_PATH}${next}`} />
  }
  if (onSignInPage) {
    return <Navigate replace to={safeNext(new URLSearchParams(location.search).get('next'))} />
  }
  return <MeContext value={session.data}>{children}</MeContext>
}

/** The signed-in person. Throws outside a signed-in tree, so screens never see "nobody". */
export function useMe(): Me {
  const me = useContext(MeContext)
  if (me === null) {
    throw new Error('useMe must be used inside a signed-in tree (below SessionProvider)')
  }
  return me
}

/**
 * Returns a function that posts /api/v1/auth/logout and shows the sign-in page.
 * It rejects with the ApiError when the server could not be told, so the caller can say so.
 */
export function useSignOut(): () => Promise<void> {
  const queryClient = useQueryClient()
  return useCallback(async () => {
    try {
      await api<void>('POST', '/api/v1/auth/logout')
    } catch (error) {
      // 401 means the session was already gone, which is what was wanted.
      if (!(error instanceof ApiError && error.status === 401)) {
        throw error
      }
    }
    endSession(queryClient)
  }, [queryClient])
}
