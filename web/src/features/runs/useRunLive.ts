// Everything the run page knows about one run: the run itself, its log lines and how the live connection is.
//
// A finished run is read once: GET the run, then the whole log in pages of 1000. An active run is watched:
// GET the run, then an EventSource on its stream. `state` events change the run (they carry no steps, so they
// are merged into what was loaded), `log` events append lines, `end` closes the stream. If the connection
// breaks the browser reconnects by itself with Last-Event-ID; if it gives up (readyState CLOSED) this hook
// opens a new stream after a pause, from the last cursor it saw. A line already held (same leg and seq) is
// never added twice, so a resent batch changes nothing.
import { useEffect, useReducer } from 'react'
import { api, nullListsToEmpty } from '../../api/client'
import { ApiError } from '../../api/error'
import type {
  ApiLogGap,
  ApiLogLine,
  LogGap,
  LogLine,
  LogPage,
  RunConnection,
  RunDetail,
  RunLive,
  RunStateEvent,
} from '../../api/types'
import { isFinalRunState } from '../../api/types'

export type { RunLive }

export const MAX_LINES = 20_000
export const LOG_PAGE_SIZE = 1000
/** How long to wait before opening a new stream after the browser gave up on one. */
export const REOPEN_DELAY_MS = 2000

interface State {
  run: RunDetail | undefined
  lines: LogLine[]
  gaps: LogGap[]
  connection: RunConnection
  truncated: boolean
  error: ApiError | undefined
}

type Action =
  | { type: 'reset' }
  | { type: 'loaded'; run: RunDetail }
  | { type: 'state'; event: RunStateEvent }
  | { type: 'lines'; lines: LogLine[]; gaps: LogGap[] }
  | { type: 'connection'; connection: RunConnection }
  | { type: 'error'; error: ApiError }

const initial: State = { run: undefined, lines: [], gaps: [], connection: 'live', truncated: false, error: undefined }

const timeOf = (line: LogLine) => Date.parse(line.at)

/** Time, then leg, then seq. */
function compareLines(a: LogLine, b: LogLine): number {
  return timeOf(a) - timeOf(b) || a.legId - b.legId || a.seq - b.seq
}

function mergeLines(existing: LogLine[], added: LogLine[]): { lines: LogLine[]; truncated: boolean } {
  const last = existing[existing.length - 1]
  const sortedAdded = [...added].sort(compareLines)
  const first = sortedAdded[0]
  const inOrder = last === undefined || first === undefined || compareLines(last, first) <= 0
  let merged = inOrder ? existing.concat(sortedAdded) : existing.concat(sortedAdded).sort(compareLines)
  let truncated = false
  if (merged.length > MAX_LINES) {
    merged = merged.slice(merged.length - MAX_LINES)
    truncated = true
  }
  return { lines: merged, truncated }
}

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case 'reset':
      return initial
    case 'loaded':
      return { ...state, run: action.run }
    case 'state':
      // The event has no steps, selection or approval: keep what was loaded.
      return state.run ? { ...state, run: { ...state.run, ...action.event }, connection: state.connection === 'finished' ? 'finished' : 'live' } : state
    case 'lines': {
      const merged = action.lines.length > 0 ? mergeLines(state.lines, action.lines) : { lines: state.lines, truncated: false }
      const known = new Set(state.gaps.map((gap) => `${gap.legId}:${gap.fromSeq}:${gap.toSeq}`))
      const gaps = state.gaps.concat(action.gaps.filter((gap) => !known.has(`${gap.legId}:${gap.fromSeq}:${gap.toSeq}`)))
      return {
        ...state,
        lines: merged.lines,
        gaps,
        truncated: state.truncated || merged.truncated,
        connection: state.connection === 'finished' ? 'finished' : 'live',
      }
    }
    case 'connection':
      return { ...state, connection: action.connection }
    case 'error':
      return { ...state, error: action.error, connection: 'finished' }
  }
}

const toLine = (runId: number) => (line: ApiLogLine): LogLine => ({
  runId,
  legId: line.leg_id,
  seq: line.seq,
  at: line.at,
  level: line.level,
  text: line.text,
})

const toGap = (gap: ApiLogGap): LogGap => ({ legId: gap.leg_id, fromSeq: gap.from_seq, toSeq: gap.to_seq })

function asApiError(caught: unknown): ApiError {
  return caught instanceof ApiError ? caught : new ApiError(0, 'network', 'Something went wrong. Try again.')
}

/** The body of a stream event, with the nulls Go sends for empty lists turned into []. */
function parse<T>(event: Event): T | undefined {
  try {
    return nullListsToEmpty(JSON.parse((event as MessageEvent<string>).data)) as T
  } catch {
    return undefined
  }
}

export function useRunLive(runId: number, opts: { logs: boolean }): RunLive {
  const [state, dispatch] = useReducer(reducer, initial)
  const wantLogs = opts.logs

  useEffect(() => {
    let cancelled = false
    let source: EventSource | undefined
    let reopenTimer: ReturnType<typeof setTimeout> | undefined
    // (legId, seq) of every line taken so far, also of lines the 20,000 cap dropped.
    const seen = new Set<string>()
    let cursor = ''
    dispatch({ type: 'reset' })

    const takeLines = (page: LogPage) => {
      const fresh: LogLine[] = []
      for (const line of page.lines) {
        const key = `${line.leg_id}:${line.seq}`
        if (!seen.has(key)) {
          seen.add(key)
          fresh.push(toLine(runId)(line))
        }
      }
      dispatch({ type: 'lines', lines: fresh, gaps: page.gaps.map(toGap) })
    }

    const readWholeLog = async () => {
      let after = ''
      for (;;) {
        const query = `limit=${LOG_PAGE_SIZE}${after === '' ? '' : `&after=${encodeURIComponent(after)}`}`
        const page = await api<LogPage>('GET', `/api/v1/runs/${runId}/log?${query}`)
        if (cancelled) return
        takeLines(page)
        if (page.lines.length < LOG_PAGE_SIZE || page.next === '' || page.next === after) return
        after = page.next
      }
    }

    const openStream = () => {
      const suffix = cursor === '' ? '' : `?after=${encodeURIComponent(cursor)}`
      const stream = new EventSource(`/api/v1/runs/${runId}/events${suffix}`)
      source = stream
      stream.addEventListener('open', () => dispatch({ type: 'connection', connection: 'live' }))
      stream.addEventListener('state', (event) => {
        const body = parse<RunStateEvent>(event)
        if (body) dispatch({ type: 'state', event: body })
      })
      stream.addEventListener('log', (event) => {
        const body = parse<LogPage>(event)
        if (!body) return
        const id = (event as MessageEvent).lastEventId
        if (id) cursor = id
        if (wantLogs) takeLines(body)
        else dispatch({ type: 'connection', connection: 'live' })
      })
      stream.addEventListener('end', (event) => {
        const body = parse<RunStateEvent>(event)
        if (body) dispatch({ type: 'state', event: body })
        stream.close()
        dispatch({ type: 'connection', connection: 'finished' })
      })
      stream.addEventListener('error', () => {
        dispatch({ type: 'connection', connection: 'reconnecting' })
        // CONNECTING: the browser is retrying by itself. CLOSED: it gave up, so ask again after a pause.
        if (stream.readyState === EventSource.CLOSED && !cancelled) {
          stream.close()
          reopenTimer = setTimeout(openStream, REOPEN_DELAY_MS)
        }
      })
    }

    const start = async () => {
      let run: RunDetail
      try {
        run = await api<RunDetail>('GET', `/api/v1/runs/${runId}`)
      } catch (caught) {
        if (!cancelled) dispatch({ type: 'error', error: asApiError(caught) })
        return
      }
      if (cancelled) return
      dispatch({ type: 'loaded', run })
      if (isFinalRunState(run.state)) {
        try {
          if (wantLogs) await readWholeLog()
        } catch (caught) {
          if (!cancelled) dispatch({ type: 'error', error: asApiError(caught) })
          return
        }
        if (!cancelled) dispatch({ type: 'connection', connection: 'finished' })
        return
      }
      openStream()
    }

    void start()
    return () => {
      cancelled = true
      if (reopenTimer) clearTimeout(reopenTimer)
      source?.close()
    }
  }, [runId, wantLogs])

  return {
    run: state.run,
    lines: state.lines,
    gaps: state.gaps,
    connection: wantLogs ? state.connection : 'no-access',
    truncated: state.truncated,
    error: state.error,
  }
}
