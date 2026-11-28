import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useState } from 'react'
import { BrowserRouter } from 'react-router-dom'
import { createQueryClient } from './api/queryClient'
import { SessionProvider } from './auth/session'

/** Query client, router and session, in the order the app needs them. */
export function AppProviders({ children }: { children: ReactNode }) {
  const [queryClient] = useState(createQueryClient)
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <SessionProvider>{children}</SessionProvider>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
