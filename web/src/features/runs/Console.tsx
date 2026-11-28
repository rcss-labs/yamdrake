// The dark log panel. Scope, text filter and errors only are applied here, in the browser, over the lines
// useRunLive holds; nothing is copied into state. Log text is only ever a React text node.
import { memo, useDeferredValue, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { LogLine, RunDetail, RunLive } from '../../api/types'
import { isFinalRunState } from '../../api/types'
import { buildConsoleView, logDownloadPath, scopeTitle, type ConsoleRow } from './consoleModel'
import type { ConsoleScope } from './scope'
import { MAX_LINES } from './useRunLive'
import './console.css'

/** How far from the bottom (px) still counts as "at the bottom". */
const BOTTOM_SLACK = 24

const pad = (value: number) => String(value).padStart(2, '0')

function clockTime(iso: string): string {
  const at = new Date(iso)
  return Number.isNaN(at.getTime()) ? '' : `${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`
}

const ConsoleLineRow = memo(function ConsoleLineRow({ line, number, prefix }: { line: LogLine; number: number; prefix: string }) {
  return (
    <div className={`con-ln con-ln-${line.level}`}>
      <span className="con-no">{number}</span>
      <span className="con-ts">{clockTime(line.at)}</span>
      <span className="con-tx">
        {prefix}
        {line.text}
      </span>
    </div>
  )
})

function ConsoleRowView({ row }: { row: ConsoleRow }) {
  if (row.kind === 'gap') {
    return <div className="con-gap">{`Lines ${row.fromSeq} to ${row.toSeq} of this ${row.ofStep ? 'step' : 'log'} are missing`}</div>
  }
  return <ConsoleLineRow line={row.line} number={row.number} prefix={row.prefix} />
}

export function Console({ run, live, scope }: { run: RunDetail; live: RunLive; scope: ConsoleScope }) {
  const [query, setQuery] = useState('')
  const [errorsOnly, setErrorsOnly] = useState(false)
  const deferredQuery = useDeferredValue(query)
  const noAccess = live.connection === 'no-access'
  const active = !isFinalRunState(run.state)

  const view = useMemo(
    () => buildConsoleView(run.legs, live.lines, live.gaps, scope, deferredQuery, errorsOnly),
    [run.legs, live.lines, live.gaps, scope, deferredQuery, errorsOnly],
  )

  // Follow the tail: stay at the bottom while the person is there, leave the view alone once they scrolled up.
  const bodyRef = useRef<HTMLDivElement>(null)
  const followRef = useRef(true)
  const [atBottom, setAtBottom] = useState(true)
  // A new scope starts at the tail again.
  const scopeKey = `${scope.kind}:${scope.kind === 'step' ? scope.step : scope.kind === 'service' ? scope.service : ''}`
  const [seenScopeKey, setSeenScopeKey] = useState(scopeKey)
  if (seenScopeKey !== scopeKey) {
    setSeenScopeKey(scopeKey)
    setAtBottom(true)
  }

  useLayoutEffect(() => {
    followRef.current = true
  }, [scopeKey])

  useLayoutEffect(() => {
    const body = bodyRef.current
    if (body && active && followRef.current) body.scrollTop = body.scrollHeight
  }, [view, active])

  const onScroll = () => {
    const body = bodyRef.current
    if (!body) return
    const nearBottom = body.scrollHeight - body.scrollTop - body.clientHeight < BOTTOM_SLACK
    followRef.current = nearBottom
    setAtBottom(nearBottom)
  }

  const jumpToLatest = () => {
    const body = bodyRef.current
    if (body) body.scrollTop = body.scrollHeight
    followRef.current = true
    setAtBottom(true)
  }

  const downloadPath = logDownloadPath(run.id, scope)
  const filtering = query.trim() !== '' || errorsOnly
  const errorWord = view.errors === 1 ? 'error line' : 'error lines'

  return (
    <section className="con" aria-label="Console">
      <div className="con-h">
        <div className="con-title">
          <span className="mono">{scopeTitle(scope)}</span>
          {!noAccess && <span className="con-count">{`${view.shown} of ${view.total} lines, ${view.errors} ${errorWord}`}</span>}
        </div>
        {!noAccess && (
          <div className="con-tools">
            <label className="con-search">
              <input type="search" placeholder="Filter lines" aria-label="Filter log lines" value={query} onChange={(event) => setQuery(event.target.value)} />
            </label>
            <button type="button" className={`con-tog${errorsOnly ? ' on' : ''}`} aria-pressed={errorsOnly} onClick={() => setErrorsOnly((on) => !on)}>
              Errors only
            </button>
            <a className="con-dl" href={downloadPath} download>
              Download
            </a>
          </div>
        )}
      </div>
      {live.connection === 'reconnecting' && (
        <div className="con-strip" role="status">
          Reconnecting
        </div>
      )}
      {noAccess && (
        <div className="con-strip" role="status">
          You do not have access to logs in this environment
        </div>
      )}
      {live.truncated && !noAccess && (
        <div className="con-strip con-strip-note">
          {`Only the latest ${MAX_LINES.toLocaleString('en-US')} lines are shown. `}
          <a href={downloadPath} download>
            Download the log
          </a>
        </div>
      )}
      <div className="con-body-wrap">
        <div ref={bodyRef} className="con-body" role="log" aria-label="Log lines" aria-live="off" onScroll={onScroll}>
          {view.rows.map((row) => (
            <ConsoleRowView key={row.key} row={row} />
          ))}
          {!noAccess && view.rows.length === 0 && (
            <div className="con-empty">{view.total > 0 && filtering ? 'No lines match' : 'No log lines yet'}</div>
          )}
        </div>
        {active && !atBottom && !noAccess && (
          <button type="button" className="con-jump" onClick={jumpToLatest}>
            Jump to latest
          </button>
        )}
      </div>
    </section>
  )
}
