// Download of a scope's log as a text file. The log endpoint answers JSON pages of at most 1000 lines, so
// this follows `next` until a short page, then writes one text line per log line. The text goes into a
// Blob of type text/plain; it is never put into the page as HTML.
import { api } from '../../api/client'
import type { LogLine, LogPage, RunDetail } from '../../api/types'
import { buildConsoleView, LOG_PAGE_LIMIT, logPagePath } from './consoleModel'
import type { ConsoleScope } from './scope'

const MAX_PAGES = 2000
/** How long a saved file's object URL stays valid, so a slow browser still gets the data. */
const REVOKE_DELAY_MS = 10_000

export async function fetchLogText(run: RunDetail, scope: ConsoleScope, maxPages = MAX_PAGES): Promise<string> {
  const lines: LogLine[] = []
  // The reader reports gaps per page, so they are collected from every page, once each.
  const gaps = new Map<string, LogPage['gaps'][number]>()
  let after = ''
  let stoppedAtLimit = false
  for (let page = 0; page < maxPages; page += 1) {
    const result = await api<LogPage>('GET', logPagePath(run.id, scope, after))
    for (const gap of result.gaps ?? []) gaps.set(`${gap.leg_id}:${gap.from_seq}:${gap.to_seq}`, gap)
    for (const line of result.lines ?? []) {
      lines.push({ runId: run.id, legId: line.leg_id, seq: line.seq, at: line.at, level: line.level, text: line.text })
    }
    if ((result.lines ?? []).length < LOG_PAGE_LIMIT || !result.next || result.next === after) break
    after = result.next
    stoppedAtLimit = page === maxPages - 1
  }
  const view = buildConsoleView(
    run.legs,
    lines,
    [...gaps.values()].map((gap) => ({ legId: gap.leg_id, fromSeq: gap.from_seq, toSeq: gap.to_seq })),
    scope,
    '',
    false,
  )
  const out = view.rows.map((row) =>
    row.kind === 'gap'
      ? `-- Lines ${row.fromSeq} to ${row.toSeq} of this ${row.ofStep ? 'step' : 'log'} are missing --`
      : `${row.line.at} ${row.line.level} ${row.prefix}${row.line.text}`,
  )
  if (stoppedAtLimit) out.push(`-- The log was cut after ${lines.length} lines; later lines are not in this file --`)
  return out.length > 0 ? `${out.join('\n')}\n` : ''
}

export function logFileName(run: RunDetail, scope: ConsoleScope): string {
  const part = scope.kind === 'step' ? scope.step : scope.kind === 'service' ? scope.service : ''
  const safe = part.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '')
  return `run-${run.id}${safe ? `-${safe}` : ''}.log`
}

let pendingRevoke: { url: string; timer: ReturnType<typeof setTimeout> } | undefined

/**
 * Saves text as a file through a temporary link. The object URL is revoked after a delay so a slow browser
 * still gets the data, or at once when the next file is saved.
 */
export function saveText(text: string, fileName: string): void {
  if (pendingRevoke) {
    clearTimeout(pendingRevoke.timer)
    URL.revokeObjectURL(pendingRevoke.url)
    pendingRevoke = undefined
  }
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const timer = setTimeout(() => {
    URL.revokeObjectURL(url)
    if (pendingRevoke?.url === url) pendingRevoke = undefined
  }, REVOKE_DELAY_MS)
  pendingRevoke = { url, timer }
  const link = document.createElement('a')
  link.href = url
  link.download = fileName
  link.click()
}
