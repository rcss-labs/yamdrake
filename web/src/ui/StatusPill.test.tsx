import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { StatusPill } from './StatusPill'
import { legStatusLabels, runStatusLabels, type LegStatus, type RunStatus } from './status'

// Hard-coded from the brief so a status dropped from the label maps fails here.
const runExpected: Record<RunStatus, string> = {
  resolving: 'Planning',
  planned: 'Planned',
  waiting_approval: 'Waiting for approval',
  queued: 'Queued',
  running: 'Running',
  cancelling: 'Cancelling',
  needs_reconciliation: 'Needs reconciliation',
  succeeded: 'Succeeded',
  partially_succeeded: 'Partly succeeded',
  failed: 'Failed',
  cancelled: 'Cancelled',
  rejected: 'Rejected',
  expired: 'Expired',
  invalidated: 'Plan out of date',
}
const legExpected: Record<LegStatus, string> = {
  pending: 'Not started',
  queued: 'Queued',
  claimed: 'Starting',
  running: 'Running',
  succeeded: 'Succeeded',
  failed: 'Failed',
  skipped: 'Skipped',
  cancelled: 'Cancelled',
  unknown: 'Unknown',
}

function shapeOf(status: Parameters<typeof StatusPill>[0]['status']) {
  const { container } = render(<StatusPill status={status} />)
  return {
    text: container.textContent,
    shape: container.querySelector('[data-shape]')?.getAttribute('data-shape'),
    tone: container.firstElementChild?.getAttribute('data-tone'),
  }
}

describe('StatusPill', () => {
  it('label maps cover exactly the brief', () => {
    expect(runStatusLabels).toEqual(runExpected)
    expect(legStatusLabels).toEqual(legExpected)
  })

  it.each(Object.entries(runExpected))('run status %s shows "%s" with a shape and tone', (status, label) => {
    const { text, shape, tone } = shapeOf(status as RunStatus)
    expect(text).toBe(label)
    expect(shape).toBeTruthy()
    expect(tone).toMatch(/^(ok|bad|run|wait|neutral)$/)
  })

  it.each(Object.entries(legExpected))('leg status %s shows "%s" with a shape and tone', (status, label) => {
    const { text, shape, tone } = shapeOf(status as LegStatus)
    expect(text).toBe(label)
    expect(shape).toBeTruthy()
    expect(tone).toMatch(/^(ok|bad|run|wait|neutral)$/)
  })

  it('shows "Never run" style label override and an empty ring for never', () => {
    const { container } = render(<StatusPill status="never" label="Never run" />)
    expect(container).toHaveTextContent('Never run')
    expect(container.querySelector('[data-shape]')).toHaveAttribute('data-shape', 'empty-ring')
  })

  it('draws different shapes for succeeded, failed, running and waiting', () => {
    const shapes = (['succeeded', 'failed', 'running', 'waiting_approval'] as const).map((s) => shapeOf(s).shape)
    expect(new Set(shapes).size).toBe(4)
  })

  it('maps the brief shapes', () => {
    expect(shapeOf('succeeded').shape).toBe('filled-circle')
    expect(shapeOf('unknown').shape).toBe('diamond')
    expect(shapeOf('claimed').shape).toBe('ring')
    expect(shapeOf('queued').shape).toBe('bars')
    expect(shapeOf('skipped').shape).toBe('dash')
    expect(shapeOf('pending').shape).toBe('empty-ring')
    expect(shapeOf('expired').shape).toBe('square')
  })
})
