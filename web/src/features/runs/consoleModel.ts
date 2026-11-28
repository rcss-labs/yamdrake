// Pure helpers for the steps tree and the console: which leg a line belongs to, which lines a scope
// shows, and the rows the console draws. No React here, so the filtering can be memoised by its inputs.
import type { LegState, LogGap, LogLine, RunLeg } from '../../api/types'
import type { ConsoleScope } from './scope'

/** A planning leg (resolve, revalidate) has no step; its scope is the step name "resolve" or "revalidate". */
export function legInStep(leg: RunLeg, step: string): boolean {
  return leg.kind === 'step' ? leg.step === step : leg.kind === step
}

export function legInScope(leg: RunLeg, scope: ConsoleScope): boolean {
  if (scope.kind === 'step') return legInStep(leg, scope.step)
  // A service shows its lines from every step, config step included.
  if (scope.kind === 'service') return leg.kind === 'step' && leg.service === scope.service
  return true
}

/** Worst state wins: failed and unknown show first, then running, waiting, finished-with-nothing, done. */
export function combinedState(states: LegState[]): LegState {
  const order: LegState[] = ['failed', 'unknown', 'running', 'claimed', 'queued', 'pending']
  for (const state of order) {
    if (states.includes(state)) return state
  }
  if (states.includes('succeeded')) return 'succeeded'
  if (states.includes('cancelled')) return 'cancelled'
  return states.length > 0 ? 'skipped' : 'pending'
}

export const needsAttention = (state: LegState): boolean => state === 'failed' || state === 'unknown'

export type ConsoleRow =
  | { kind: 'line'; key: string; line: LogLine; number: number; prefix: string }
  | { kind: 'gap'; key: string; fromSeq: number; toSeq: number; ofStep: boolean }

export interface ConsoleView {
  rows: ConsoleRow[]
  /** Lines in the scope, before the text filter and errors only. */
  total: number
  /** Lines shown. */
  shown: number
  /** Error lines in the scope, before the text filter and errors only. */
  errors: number
}

/**
 * The rows for a scope. Counting and filtering happen in one pass over the held lines. A line whose leg
 * is unknown, and a run-level line (leg 0), belong to the whole run only. Gaps are shown only while no
 * text filter and no errors-only is on, since a gap is not a line a filter could match.
 */
export function buildConsoleView(
  runLegs: RunLeg[],
  lines: LogLine[],
  gaps: LogGap[],
  scope: ConsoleScope,
  query: string,
  errorsOnly: boolean,
): ConsoleView {
  const legs = new Map<number, RunLeg>()
  for (const leg of runLegs) legs.set(leg.id, leg)

  const needle = query.trim().toLowerCase()
  const filtering = needle !== '' || errorsOnly
  const pendingGaps = new Map<number, LogGap[]>()
  if (!filtering) {
    for (const gap of gaps) {
      const leg = legs.get(gap.legId)
      if (scope.kind !== 'run' && !(leg && legInScope(leg, scope))) continue
      const list = pendingGaps.get(gap.legId) ?? []
      list.push(gap)
      pendingGaps.set(gap.legId, list)
    }
    for (const list of pendingGaps.values()) list.sort((a, b) => a.fromSeq - b.fromSeq)
  }

  const rows: ConsoleRow[] = []
  let total = 0
  let errors = 0
  let shown = 0
  const emitGap = (gap: LogGap) => {
    const leg = legs.get(gap.legId)
    rows.push({ kind: 'gap', key: `gap-${gap.legId}-${gap.fromSeq}`, fromSeq: gap.fromSeq, toSeq: gap.toSeq, ofStep: leg?.kind === 'step' })
  }

  for (const line of lines) {
    const leg = line.legId === 0 ? undefined : legs.get(line.legId)
    if (scope.kind !== 'run' && !(leg && legInScope(leg, scope))) continue
    total += 1
    const isError = line.level === 'error'
    if (isError) errors += 1

    const waiting = pendingGaps.get(line.legId)
    while (waiting && waiting.length > 0 && waiting[0]!.toSeq < line.seq) emitGap(waiting.shift() as LogGap)

    if (errorsOnly && !isError) continue
    if (needle !== '' && !line.text.toLowerCase().includes(needle)) continue
    shown += 1
    const prefix = scope.kind === 'run' && leg && leg.kind === 'step' ? `[${leg.step}${leg.service ? ` / ${leg.service}` : ''}] ` : ''
    rows.push({ kind: 'line', key: `${line.legId}-${line.seq}`, line, number: total, prefix })
  }
  for (const waiting of pendingGaps.values()) {
    for (const gap of waiting) emitGap(gap)
  }
  return { rows, total, shown, errors }
}

/** The title of the console for a scope. */
export function scopeTitle(scope: ConsoleScope): string {
  if (scope.kind === 'step') return scope.step
  if (scope.kind === 'service') return scope.service
  return 'Whole run'
}

/** The log endpoint for a scope. The Go endpoint answers JSON pages of at most 1000 lines. */
export function logDownloadPath(runId: number, scope: ConsoleScope): string {
  const params = new URLSearchParams()
  if (scope.kind === 'step' && scope.step !== 'resolve' && scope.step !== 'revalidate') params.set('step', scope.step)
  if (scope.kind === 'service') params.set('service', scope.service)
  params.set('limit', '1000')
  return `/api/v1/runs/${runId}/log?${params.toString()}`
}
