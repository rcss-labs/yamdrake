import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ReactElement } from 'react'
import { useEffect } from 'react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { SessionProvider } from '../auth/session'
import type { Me } from '../api/types'
import { signInAs, signOut } from './handlers'

interface RenderAppOptions {
  /** The address the router starts at (path and query). Default "/". */
  route?: string
  /**
   * Who the mock server believes is signed in. A Me signs that person in; null signs everybody out
   * (the sign-in page shows). Left out, the fixture default stays: signed in as adminMe.
   */
  me?: Me | null
}

interface RenderedLocation {
  pathname: string
  search: string
  hash: string
}

// Reports the router's current location to the test, outside the session provider so it is always present.
function LocationProbe({ onChange }: { onChange: (location: RenderedLocation) => void }) {
  const { pathname, search, hash } = useLocation()
  useEffect(() => {
    onChange({ pathname, search, hash })
  }, [onChange, pathname, search, hash])
  return null
}

/**
 * Renders a screen the way the app does: fresh QueryClient (no retries), MemoryRouter at `route`, and the
 * session provider, signed in as `me`. Returns the usual Testing Library result plus:
 *   queryClient   the client in use (to inspect or seed the cache)
 *   location()    the router's current { pathname, search, hash }
 */
export function renderApp(ui: ReactElement, { route = '/', me }: RenderAppOptions = {}) {
  if (me === null) {
    signOut()
  } else if (me !== undefined) {
    signInAs(me)
  }
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  let current: RenderedLocation = { pathname: '', search: '', hash: '' }
  const remember = (location: RenderedLocation) => {
    current = location
  }
  const wrap = (children: ReactElement) => (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[route]}>
        <LocationProbe onChange={remember} />
        <SessionProvider>{children}</SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>
  )
  const result = render(wrap(ui))
  return {
    ...result,
    // Rendering again keeps the same router, session and cache.
    rerender: (next: ReactElement) => result.rerender(wrap(next)),
    queryClient,
    location: () => current,
  }
}

