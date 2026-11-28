import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiLogLine, LogPage, RunDetail } from '../../api/types'
import { mockState, runIds } from '../../test/server'
import { server } from '../../test/server'
import { fetchLogText, saveText } from './downloadLog'
import { wholeRun } from './scope'

const run = (): RunDetail => {
  const found = mockState().runs.find((candidate) => candidate.id === runIds.partiallySucceeded)
  if (!found) throw new Error('no such run')
  return found
}

const apiLine = (seq: number): ApiLogLine => ({ leg_id: 10504, seq, at: '2026-11-27T10:03:00Z', level: 'info', text: `line ${seq}` })
const page = (from: number, count: number, next: string, gaps: LogPage['gaps'] = []): LogPage => ({
  lines: Array.from({ length: count }, (_, index) => apiLine(from + index)),
  next,
  gaps,
})

describe('fetchLogText', () => {
  it('takes the gaps of every page, once each, and places them in order', async () => {
    const pages: Record<string, LogPage> = {
      '': page(1, 1000, 'c1000', [{ leg_id: 10504, from_seq: 2001, to_seq: 2005 }]),
      c1000: page(1101, 1000, 'c2000', [
        { leg_id: 10504, from_seq: 1001, to_seq: 1100 },
        { leg_id: 10504, from_seq: 2001, to_seq: 2005 },
      ]),
      c2000: page(2101, 10, 'c2010'),
    }
    server.use(http.get('*/api/v1/runs/105/log', ({ request }) => HttpResponse.json(pages[new URL(request.url).searchParams.get('after') ?? ''])))
    const rows = (await fetchLogText(run(), wholeRun)).trimEnd().split('\n')
    const gapRows = rows.filter((row) => row.startsWith('--'))
    expect(gapRows).toEqual(['-- Lines 1001 to 1100 of this step are missing --', '-- Lines 2001 to 2005 of this step are missing --'])
    expect(rows.indexOf(gapRows[0] as string)).toBe(1000) // after line 1000, before line 1101
    expect(rows[1001]).toContain('line 1101')
    expect(rows).toHaveLength(2010 + 2)
  })

  it('says so in the file when it stopped at the page limit', async () => {
    server.use(http.get('*/api/v1/runs/105/log', ({ request }) => {
      const after = Number((new URL(request.url).searchParams.get('after') ?? 'c0').slice(1))
      return HttpResponse.json(page(after + 1, 1000, `c${after + 1000}`))
    }))
    const text = await fetchLogText(run(), wholeRun, 2)
    const rows = text.trimEnd().split('\n')
    expect(rows).toHaveLength(2001)
    expect(rows[2000]).toBe('-- The log was cut after 2000 lines; later lines are not in this file --')
  })
})

describe('saveText', () => {
  let revoked: string[]
  beforeEach(() => {
    vi.useFakeTimers()
    revoked = []
    let count = 0
    URL.createObjectURL = () => `blob:test/${(count += 1)}`
    URL.revokeObjectURL = (url: string) => {
      revoked.push(url)
    }
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('keeps the object URL for 10 seconds, and revokes an earlier one when the next file is saved', () => {
    saveText('a', 'a.log')
    vi.advanceTimersByTime(9_000)
    expect(revoked).toEqual([])
    saveText('b', 'b.log')
    expect(revoked).toEqual(['blob:test/1'])
    vi.advanceTimersByTime(10_000)
    expect(revoked).toEqual(['blob:test/1', 'blob:test/2'])
  })
})
