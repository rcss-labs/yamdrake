// Pure helpers for the steps tree and the console: which leg a line belongs to, which lines a scope
// shows, and the rows the console draws. No React here, so the filtering can be memoised by its inputs.
import type { LegState, LogGap, LogLine, RunLeg } from '../../api/types'
import type { ConsoleScope } from './scope'

/**
 * A planning leg (resolve, revalidate) has no step; its scope is the step value ":resolve" or ":revalidate".
 * A colon cannot be part of a real step name, so these never collide with a step called "resolve".
 */
export const planningStep = (kind: 'resolve' | 'revalidate'): string => `:${kind}`
const isPlanningStep = (step: string): boolean => step.startsWith(':')

export function legInStep(leg: RunLeg, step: string): boolean {
  return leg.kind === 'step' ? leg.step === step : planningStep(leg.kind) === step
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
  /** Lines of this scope that were dropped from the front, so held lines keep their number. */
  numberOffset = 0,
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
    rows.push({ kind: 'line', key: `${line.legId}-${line.seq}`, line, number: numberOffset + total, prefix })
  }
  for (const waiting of pendingGaps.values()) {
    for (const gap of waiting) emitGap(gap)
  }
  return { rows, total, shown, errors }
}

/** The title of the console for a scope. */
export function scopeTitle(scope: ConsoleScope): string {
  if (scope.kind === 'step') return isPlanningStep(scope.step) ? scope.step.slice(1).replace(/^./, (c) => c.toUpperCase()) : scope.step
  if (scope.kind === 'service') return scope.service
  return 'Whole run'
}

export const LOG_PAGE_LIMIT = 1000

/**
 * One page of the log endpoint for a scope (JSON, at most 1000 lines). The planning scopes cannot be
 * asked of the server, so they read the whole run and are cut in the browser.
 */
export function logPagePath(runId: number, scope: ConsoleScope, after = ''): string {
  const params = new URLSearchParams()
  if (scope.kind === 'step' && !isPlanningStep(scope.step)) params.set('step', scope.step)
  if (scope.kind === 'service') params.set('service', scope.service)
  params.set('limit', String(LOG_PAGE_LIMIT))
  if (after !== '') params.set('after', after)
  return `/api/v1/runs/${runId}/log?${params.toString()}`
}

/**
 * How many lines of the scope fell off the front between two holdings of the lines. The new list starts
 * at the first line it still holds; whatever came before that line in the old list was dropped.
 */
export function countDropped(previous: LogLine[], next: LogLine[], legs: RunLeg[], scope: ConsoleScope): number {
  const first = next[0]
  if (!first || previous.length === 0) return 0
  const at = previous.findIndex((line) => line.legId === first.legId && line.seq === first.seq)
  if (at <= 0) return 0
  const byId = new Map<number, RunLeg>()
  for (const leg of legs) byId.set(leg.id, leg)
  let dropped = 0
  for (let index = 0; index < at; index += 1) {
    const line = previous[index] as LogLine
    const leg = line.legId === 0 ? undefined : byId.get(line.legId)
    if (scope.kind === 'run' || (leg && legInScope(leg, scope))) dropped += 1
  }
  return dropped
}
