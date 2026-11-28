// Which part of a run the console shows. It lives in the address (?step=name or ?service=name; neither
// means the whole run) so a link to a failed service opens on its log.
import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'

export type ConsoleScope = { kind: 'run' } | { kind: 'step'; step: string } | { kind: 'service'; service: string }

export const wholeRun: ConsoleScope = { kind: 'run' }

/** The scope an address stands for; a step wins if both are present. */
export function scopeFromSearch(params: URLSearchParams): ConsoleScope {
  const step = params.get('step')
  if (step) return { kind: 'step', step }
  const service = params.get('service')
  if (service) return { kind: 'service', service }
  return wholeRun
}

export function sameScope(a: ConsoleScope, b: ConsoleScope): boolean {
  if (a.kind !== b.kind) return false
  if (a.kind === 'step' && b.kind === 'step') return a.step === b.step
  if (a.kind === 'service' && b.kind === 'service') return a.service === b.service
  return true
}

/** The scope in the address and a setter that replaces the history entry (other parameters are kept). */
export function useConsoleScope(): [ConsoleScope, (scope: ConsoleScope) => void] {
  const [params, setParams] = useSearchParams()
  const scope = scopeFromSearch(params)
  const setScope = useCallback(
    (next: ConsoleScope) => {
      setParams(
        (current) => {
          const updated = new URLSearchParams(current)
          updated.delete('step')
          updated.delete('service')
          if (next.kind === 'step') updated.set('step', next.step)
          if (next.kind === 'service') updated.set('service', next.service)
          return updated
        },
        { replace: true },
      )
    },
    [setParams],
  )
  return [scope, setScope]
}
