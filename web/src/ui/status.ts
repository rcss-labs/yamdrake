export type RunStatus =
  | 'resolving'
  | 'planned'
  | 'waiting_approval'
  | 'queued'
  | 'running'
  | 'cancelling'
  | 'needs_reconciliation'
  | 'succeeded'
  | 'partially_succeeded'
  | 'failed'
  | 'cancelled'
  | 'rejected'
  | 'expired'
  | 'invalidated'

export type LegStatus =
  | 'pending'
  | 'queued'
  | 'claimed'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'skipped'
  | 'cancelled'
  | 'unknown'

export type StatusKey = RunStatus | LegStatus | 'never'

export type StatusShape = 'filled-circle' | 'diamond' | 'ring' | 'bars' | 'dash' | 'empty-ring' | 'square'
export type StatusTone = 'ok' | 'bad' | 'run' | 'wait' | 'neutral'

export const runStatusLabels: Record<RunStatus, string> = {
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

export const legStatusLabels: Record<LegStatus, string> = {
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

// Statuses that exist in both lists (queued, running, ...) share one shape and tone,
// so a single map keyed by name serves both.
const appearance: Record<StatusKey, { shape: StatusShape; tone: StatusTone }> = {
  resolving: { shape: 'bars', tone: 'neutral' },
  planned: { shape: 'bars', tone: 'neutral' },
  waiting_approval: { shape: 'bars', tone: 'wait' },
  queued: { shape: 'bars', tone: 'neutral' },
  running: { shape: 'ring', tone: 'run' },
  claimed: { shape: 'ring', tone: 'run' },
  cancelling: { shape: 'ring', tone: 'wait' },
  needs_reconciliation: { shape: 'diamond', tone: 'wait' },
  succeeded: { shape: 'filled-circle', tone: 'ok' },
  partially_succeeded: { shape: 'diamond', tone: 'bad' },
  failed: { shape: 'diamond', tone: 'bad' },
  unknown: { shape: 'diamond', tone: 'wait' },
  cancelled: { shape: 'square', tone: 'neutral' },
  rejected: { shape: 'square', tone: 'neutral' },
  expired: { shape: 'square', tone: 'neutral' },
  invalidated: { shape: 'square', tone: 'neutral' },
  skipped: { shape: 'dash', tone: 'neutral' },
  pending: { shape: 'empty-ring', tone: 'neutral' },
  never: { shape: 'empty-ring', tone: 'neutral' },
}

export function statusShape(status: StatusKey): StatusShape {
  return appearance[status].shape
}

export function statusTone(status: StatusKey): StatusTone {
  return appearance[status].tone
}

// Plain-words name. "Never" is only used for jobs that have not run yet.
export function statusLabel(status: StatusKey): string {
  if (status === 'never') return 'Never run'
  if (status === 'pending') return legStatusLabels.pending
  if (status === 'claimed' || status === 'skipped' || status === 'unknown') return legStatusLabels[status]
  return runStatusLabels[status]
}
