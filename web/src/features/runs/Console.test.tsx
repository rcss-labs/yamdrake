import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { LogGap, LogLevel, LogLine, RunDetail, RunLive } from '../../api/types'
import { mockState, requests, runIds } from '../../test/server'
import { Console } from './Console'
import { StepsTree } from './StepsTree'
import { wholeRun, type ConsoleScope } from './scope'
import { useRunLive } from './useRunLive'

const runOf = (id: number): RunDetail => {
  const run = mockState().runs.find((candidate) => candidate.id === id)
  if (!run) throw new Error('no such run')
  return run
}

// Run 105 legs: 10501 resolve, 10502 revalidate, 10503/10504 copy-config backend-api/billing,
// 10505/10506 deploy, 10507/10508 verify. Leg 0 is a run-level line.
const line = (legId: number, seq: number, text: string, level: LogLevel = 'info', runId = 105): LogLine => ({
  runId,
  legId,
  seq,
  at: '2026-11-27T10:03:00Z',
  level,
  text,
})

const liveOf = (run: RunDetail, lines: LogLine[], extra: Partial<RunLive> = {}): RunLive => ({
  run,
  lines,
  gaps: [],
  connection: 'finished',
  truncated: false,
  error: undefined,
  reload: () => {},
  ...extra,
})

const rowsOf = (container: HTMLElement) => Array.from(container.querySelectorAll('.con-ln')).map((row) => row.textContent ?? '')

const sample: LogLine[] = [
  line(0, 1, 'run started'),
  line(10503, 1, 'backend-api: copied'),
  line(10504, 1, 'billing: reading source'),
  line(10504, 2, 'billing: NoSuchKey', 'error'),
  line(10506, 1, 'billing: deploy skipped', 'warn'),
  line(10505, 1, 'backend-api: deployed', 'ok'),
]

describe('Console scope and counts', () => {
  const run = runOf(runIds.partiallySucceeded)

  it('shows every line in the whole run, with the counts in the header', () => {
    const { container } = render(<Console run={run} live={liveOf(run, sample)} scope={wholeRun} />)
    expect(rowsOf(container)).toHaveLength(6)
    expect(screen.getByText('Whole run')).toBeInTheDocument()
    expect(screen.getByText('6 of 6 lines, 1 error line')).toBeInTheDocument()
  })

  it('shows only the lines of a step', () => {
    const { container } = render(<Console run={run} live={liveOf(run, sample)} scope={{ kind: 'step', step: 'copy-config' }} />)
    expect(rowsOf(container).map((text) => text.replace(/^.*?(backend|billing)/, '$1'))).toEqual([
      'backend-api: copied',
      'billing: reading source',
      'billing: NoSuchKey',
    ])
    expect(screen.getByText('3 of 3 lines, 1 error line')).toBeInTheDocument()
  })

  it('shows a service with its lines from every step, including its config step', () => {
    const { container } = render(<Console run={run} live={liveOf(run, sample)} scope={{ kind: 'service', service: 'billing' }} />)
    const text = rowsOf(container).join('\n')
    expect(rowsOf(container)).toHaveLength(3)
    expect(text).toContain('billing: reading source')
    expect(text).toContain('billing: NoSuchKey')
    expect(text).toContain('billing: deploy skipped')
    expect(text).not.toContain('backend-api')
    expect(text).not.toContain('run started')
  })

  it('combines the text filter with errors only, and counts shown against all lines of the scope', async () => {
    const { container } = render(<Console run={run} live={liveOf(run, sample)} scope={{ kind: 'service', service: 'billing' }} />)
    await userEvent.type(screen.getByRole('searchbox', { name: 'Filter log lines' }), 'NOSUCH')
    await waitFor(() => expect(rowsOf(container)).toHaveLength(1))
    expect(screen.getByText('1 of 3 lines, 1 error line')).toBeInTheDocument()

    await userEvent.clear(screen.getByRole('searchbox', { name: 'Filter log lines' }))
    await userEvent.type(screen.getByRole('searchbox', { name: 'Filter log lines' }), 'billing')
    await userEvent.click(screen.getByRole('button', { name: 'Errors only' }))
    await waitFor(() => expect(rowsOf(container)).toHaveLength(1))
    expect(screen.getByRole('button', { name: 'Errors only' })).toHaveAttribute('aria-pressed', 'true')

    await userEvent.clear(screen.getByRole('searchbox', { name: 'Filter log lines' }))
    await userEvent.type(screen.getByRole('searchbox', { name: 'Filter log lines' }), 'deploy skipped')
    await waitFor(() => expect(screen.getByText('No lines match')).toBeInTheDocument())
  })

  it('numbers the lines of the scope, tints errors, and tells a step prefix in the whole run', () => {
    const { container } = render(<Console run={run} live={liveOf(run, sample)} scope={wholeRun} />)
    const rows = container.querySelectorAll('.con-ln')
    expect(rows[3]).toHaveClass('con-ln-error')
    expect(rows[4]).toHaveClass('con-ln-warn')
    expect(rows[5]).toHaveClass('con-ln-ok')
    expect(rows[3]?.querySelector('.con-no')).toHaveTextContent('4')
    expect(rows[3]?.textContent).toContain('[copy-config / billing]')
  })
})

describe('Console gaps', () => {
  it('renders a gap as its own row before the first line after it, and hides it while filtering', async () => {
    const run = runOf(runIds.partiallySucceeded)
    const gaps: LogGap[] = [{ legId: 10504, fromSeq: 2, toSeq: 5 }]
    const lines = [line(10504, 1, 'billing: first'), line(10504, 6, 'billing: after the gap')]
    const { container } = render(<Console run={run} live={liveOf(run, lines, { gaps })} scope={{ kind: 'step', step: 'copy-config' }} />)
    const rows = Array.from(container.querySelectorAll('.con-ln, .con-gap')).map((row) => row.textContent ?? '')
    expect(rows).toHaveLength(3)
    expect(rows[1]).toBe('Lines 2 to 5 of this step are missing')
    await userEvent.type(screen.getByRole('searchbox', { name: 'Filter log lines' }), 'first')
    await waitFor(() => expect(container.querySelector('.con-gap')).toBeNull())
  })
})

describe('Console text safety', () => {
  it('shows markup in a log line literally and never as elements', () => {
    const run = runOf(runIds.partiallySucceeded)
    const evil = '<script>window.__pwned = 1</script><img src=x onerror="window.__pwned = 2">'
    const { container } = render(<Console run={run} live={liveOf(run, [line(0, 1, evil)])} scope={wholeRun} />)
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('.con-tx')?.textContent).toBe(evil)
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })
})

describe('Console connection and notices', () => {
  const run = runOf(runIds.running)

  it('says nothing about the connection while live', () => {
    render(<Console run={run} live={liveOf(run, [], { connection: 'live' })} scope={wholeRun} />)
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('shows "Reconnecting" while the connection retries', () => {
    render(<Console run={run} live={liveOf(run, [], { connection: 'reconnecting' })} scope={wholeRun} />)
    expect(screen.getByRole('status')).toHaveTextContent('Reconnecting')
  })

  it('tells a person without log access, and makes no log request', async () => {
    function Harness() {
      const live = useRunLive(runIds.running, { logs: false })
      return live.run ? <Console run={live.run} live={live} scope={wholeRun} /> : null
    }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={client}>
        <Harness />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('You do not have access to logs in this environment')).toBeInTheDocument()
    expect(requests.filter((request) => request.path.endsWith('/log'))).toEqual([])
    expect(screen.queryByRole('link', { name: 'Download' })).toBeNull()
  })

  it('shows a notice with the download link when the oldest lines were dropped', () => {
    const finished = runOf(runIds.partiallySucceeded)
    render(<Console run={finished} live={liveOf(finished, sample, { truncated: true })} scope={wholeRun} />)
    const notice = screen.getByText(/Only the latest 20,000 lines are shown/)
    expect(notice).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: /Download/ }).length).toBeGreaterThanOrEqual(2)
  })

  it('links Download to the log endpoint for the scope', () => {
    const finished = runOf(runIds.partiallySucceeded)
    const { rerender } = render(<Console run={finished} live={liveOf(finished, sample)} scope={wholeRun} />)
    expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute('href', '/api/v1/runs/105/log?limit=1000')
    rerender(<Console run={finished} live={liveOf(finished, sample)} scope={{ kind: 'step', step: 'copy-config' }} />)
    expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute('href', '/api/v1/runs/105/log?step=copy-config&limit=1000')
    rerender(<Console run={finished} live={liveOf(finished, sample)} scope={{ kind: 'service', service: 'billing' }} />)
    expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute('href', '/api/v1/runs/105/log?service=billing&limit=1000')
  })
})

describe('Console follow tail', () => {
  /** Gives the log body a scroll geometry (jsdom has none) and returns it. */
  function geometry(scrollTop: number): HTMLElement {
    const body = screen.getByRole('log')
    Object.defineProperty(body, 'scrollHeight', { configurable: true, value: 1000 })
    Object.defineProperty(body, 'clientHeight', { configurable: true, value: 200 })
    Object.defineProperty(body, 'scrollTop', { configurable: true, writable: true, value: scrollTop })
    fireEvent.scroll(body)
    return body
  }
  const run = runOf(runIds.running)
  const lines = (count: number) => Array.from({ length: count }, (_, index) => line(0, index + 1, `n${index}`, 'info', 104))

  it('keeps the view at the bottom when new lines arrive and the person is at the bottom', () => {
    const { rerender } = render(<Console run={run} live={liveOf(run, lines(3), { connection: 'live' })} scope={wholeRun} />)
    const body = geometry(800)
    act(() => {
      Object.defineProperty(body, 'scrollHeight', { configurable: true, value: 1200 })
      rerender(<Console run={run} live={liveOf(run, lines(6), { connection: 'live' })} scope={wholeRun} />)
    })
    expect(body.scrollTop).toBe(1200)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
  })

  it('stays put when the person scrolled up, and offers "Jump to latest"', async () => {
    const { rerender } = render(<Console run={run} live={liveOf(run, lines(3), { connection: 'live' })} scope={wholeRun} />)
    const body = geometry(100)
    rerender(<Console run={run} live={liveOf(run, lines(6), { connection: 'live' })} scope={wholeRun} />)
    expect(body.scrollTop).toBe(100)
    await userEvent.click(screen.getByRole('button', { name: 'Jump to latest' }))
    expect(body.scrollTop).toBe(1000)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
  })

  it('does not offer "Jump to latest" for a finished run', () => {
    const finished = runOf(runIds.partiallySucceeded)
    render(<Console run={finished} live={liveOf(finished, lines(3))} scope={wholeRun} />)
    geometry(0)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
  })
})

describe('Console with thousands of lines', () => {
  it('renders 5,000 lines within 1.5 seconds, and one click on a failed service shows only its lines', async () => {
    const run = runOf(runIds.partiallySucceeded)
    const legIds = [10503, 10504, 10505, 10506, 10507, 10508]
    const lines: LogLine[] = []
    const seqs = new Map<number, number>()
    let billingFailed = 0
    for (let index = 0; index < 5000; index += 1) {
      const legId = legIds[index % legIds.length] ?? 0
      const seq = (seqs.get(legId) ?? 0) + 1
      seqs.set(legId, seq)
      if (run.legs.find((leg) => leg.id === legId)?.service === 'billing') billingFailed += 1
      lines.push(line(legId, seq, `line ${index}`))
    }

    function Harness() {
      const [scope, setScope] = useState<ConsoleScope>(wholeRun)
      return (
        <>
          <StepsTree run={run} lines={lines} scope={scope} onSelect={setScope} />
          <Console run={run} live={liveOf(run, lines)} scope={scope} />
        </>
      )
    }
    // CPU time of this process, not wall time: the suite runs on a shared, busy machine where wall time varies several-fold.
    const start = process.cpuUsage()
    const { container } = render(<Harness />)
    const used = process.cpuUsage(start)
    const elapsed = (used.user + used.system) / 1000
    expect(rowsOf(container)).toHaveLength(5000)
    expect(elapsed).toBeLessThan(1500)

    await userEvent.click(screen.getAllByRole('button', { name: /^billing/ })[0] as HTMLElement)
    const shown = rowsOf(container)
    expect(shown).toHaveLength(billingFailed)
    expect(billingFailed).toBeGreaterThan(40)
    expect(billingFailed).toBeLessThan(5000)
  }, 15000)
})
