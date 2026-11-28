import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { RunState } from '../../api/types'

export type StatusSegment = 'all' | 'failed' | 'succeeded' | 'running' | 'waiting'

export const statusOptions: { value: StatusSegment; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'failed', label: 'Failed' },
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'running', label: 'Running' },
  { value: 'waiting', label: 'Waiting' },
]

// Failed also holds partly succeeded runs and runs that need reconciliation: both need a person to look.
// Runs that were cancelled, rejected, expired or invalidated appear under All only.
export const statesBySegment: Record<StatusSegment, RunState[]> = {
  all: [],
  failed: ['failed', 'partially_succeeded', 'needs_reconciliation'],
  succeeded: ['succeeded'],
  running: ['queued', 'running', 'cancelling'],
  waiting: ['planned', 'waiting_approval', 'resolving'],
}

const isSegment = (value: string | null): value is StatusSegment => statusOptions.some((option) => option.value === value)

export interface RunFilters {
  status: StatusSegment
  environment: string
  q: string
  /** Takes any string (the Segmented control's value); an unknown one clears the filter. */
  setStatus: (status: string) => void
  setEnvironment: (environment: string) => void
  setQ: (q: string) => void
}

/** The list filters as they are kept in the address (`?status=&environment=&q=`); other parameters are left alone. */
export function useRunFilters(): RunFilters {
  const [params, setParams] = useSearchParams()
  const set = useCallback(
    (name: string, value: string) =>
      setParams(
        (current) => {
          const next = new URLSearchParams(current)
          if (value === '' || value === 'all') next.delete(name)
          else next.set(name, value)
          return next
        },
        { replace: true },
      ),
    [setParams],
  )
  const status = params.get('status')
  return {
    status: isSegment(status) ? status : 'all',
    environment: params.get('environment') ?? '',
    q: params.get('q') ?? '',
    setStatus: (value) => set('status', isSegment(value) ? value : ''),
    setEnvironment: (value) => set('environment', value),
    setQ: (value) => set('q', value),
  }
}
