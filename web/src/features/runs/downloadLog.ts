// Download of a scope's log as a text file. The log endpoint answers JSON pages of at most 1000 lines, so
// this follows `next` until a short page, then writes one text line per log line. The text goes into a
// Blob of type text/plain; it is never put into the page as HTML.
import { api } from '../../api/client'
import type { LogLine, LogPage, RunDetail } from '../../api/types'
import { buildConsoleView, LOG_PAGE_LIMIT, logPagePath } from './consoleModel'
import type { ConsoleScope } from './scope'

const MAX_PAGES = 2000

export async function fetchLogText(run: RunDetail, scope: ConsoleScope): Promise<string> {
  const lines: LogLine[] = []
  let gaps: LogPage['gaps'] = []
  let after = ''
  for (let page = 0; page < MAX_PAGES; page += 1) {
    const result = await api<LogPage>('GET', logPagePath(run.id, scope, after))
    if (page === 0) gaps = result.gaps ?? []
    for (const line of result.lines ?? []) {
      lines.push({ runId: run.id, legId: line.leg_id, seq: line.seq, at: line.at, level: line.level, text: line.text })
    }
    if ((result.lines ?? []).length < LOG_PAGE_LIMIT || !result.next || result.next === after) break
    after = result.next
  }
  const view = buildConsoleView(
    run.legs,
    lines,
    gaps.map((gap) => ({ legId: gap.leg_id, fromSeq: gap.from_seq, toSeq: gap.to_seq })),
    scope,
    '',
    false,
  )
  const out = view.rows.map((row) =>
    row.kind === 'gap'
      ? `-- Lines ${row.fromSeq} to ${row.toSeq} of this ${row.ofStep ? 'step' : 'log'} are missing --`
      : `${row.line.at} ${row.line.level} ${row.prefix}${row.line.text}`,
  )
  return out.length > 0 ? `${out.join('\n')}\n` : ''
}

export function logFileName(run: RunDetail, scope: ConsoleScope): string {
  const part = scope.kind === 'step' ? scope.step : scope.kind === 'service' ? scope.service : ''
  const safe = part.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '')
  return `run-${run.id}${safe ? `-${safe}` : ''}.log`
}

/** Saves text as a file through a temporary link, and revokes the object URL afterwards. */
export function saveText(text: string, fileName: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const link = document.createElement('a')
  link.href = url
  link.download = fileName
  link.click()
  // A moment later, so the browser has taken the data before the URL goes away.
  setTimeout(() => URL.revokeObjectURL(url), 100)
}
