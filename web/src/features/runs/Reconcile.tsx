import { useState } from 'react'
import type { RunDetail, RunLeg } from '../../api/types'
import { useCan } from '../../api/hooks'
import { useMe } from '../../auth/session'
import { Button, ConfirmDialog, DataTable, Note } from '../../ui'
import type { DataColumn } from '../../ui'
import { useCheckLegAgain, useReconcileLeg, useReleaseLocks } from './reconcileActions'
import type { ReconcileOutcome } from './reconcileActions'
import type { ConsoleScope } from './scope'
import './reconcile.css'

// The API has no "last recorded operation" for a leg, so the sentence is built from the step's `use` and the
// leg's service and version.
export function intendedOperation(leg: RunLeg, use: string): string {
  const target = `${leg.service}:${leg.version}`
  switch (use) {
    case 'deploy-ecs':
    case 'ecs-deploy':
      return `Update service to ${target}`
    case 'copy-config':
      return `Copy configuration for ${target}`
    case 'verify-health':
      return `Check ${target} is healthy`
    default:
      return `Run ${use || leg.step} for ${target}`
  }
}

type Pending = { kind: 'mark'; leg: RunLeg; outcome: ReconcileOutcome } | { kind: 'release' } | null

export function Reconcile({ run, onShowLog }: { run: RunDetail; onShowLog(scope: ConsoleScope): void }) {
  const me = useMe()
  const can = useCan()
  const canResolve = can('restore', { label: run.label, environment: run.environment, folderId: run.folder_id })
  const reconcile = useReconcileLeg(run.id)
  const checkAgain = useCheckLegAgain(run.id)
  const releaseLocks = useReleaseLocks(run.id)
  const [pending, setPending] = useState<Pending>(null)
  const [error, setError] = useState('')
  const [busyLeg, setBusyLeg] = useState<number | null>(null)

  const unknownLegs = run.legs.filter((leg) => leg.state === 'unknown')
  if (run.state !== 'needs_reconciliation' || unknownLegs.length === 0) return null

  const stepUse = (leg: RunLeg) => run.steps.find((step) => step.id === leg.step_id)?.use ?? ''
  const fail = (cause: Error) => setError(cause.message)

  const check = (leg: RunLeg) => {
    setError('')
    setBusyLeg(leg.id)
    checkAgain.mutate({ legId: leg.id }, { onError: fail, onSettled: () => setBusyLeg(null) })
  }

  const confirm = () => {
    const action = pending
    setPending(null)
    setError('')
    if (action?.kind === 'mark') {
      setBusyLeg(action.leg.id)
      reconcile.mutate({ legId: action.leg.id, outcome: action.outcome }, { onError: fail, onSettled: () => setBusyLeg(null) })
    } else if (action?.kind === 'release') {
      releaseLocks.mutate(undefined, { onError: fail })
    }
  }

  const columns: DataColumn<RunLeg>[] = [
    {
      key: 'service',
      header: 'Service',
      render: (leg) => <span className="reconcile-service">{leg.service}</span>,
    },
    { key: 'step', header: 'Step', render: (leg) => <span className="reconcile-mono">{leg.step}</span> },
    { key: 'intended', header: 'What was intended', render: (leg) => intendedOperation(leg, stepUse(leg)) },
    { key: 'detail', header: 'Why it is unknown', render: (leg) => leg.failure_detail || leg.failure_short || 'No detail recorded' },
    {
      key: 'actions',
      header: 'Actions',
      render: (leg) => (
        <div className="reconcile-actions">
          <Button size="sm" onClick={() => onShowLog({ kind: 'service', service: leg.service })}>
            Show its log
          </Button>
          {canResolve && (
            <>
              <Button size="sm" loading={busyLeg === leg.id && checkAgain.isPending} onClick={() => check(leg)}>
                Check again
              </Button>
              <Button size="sm" onClick={() => setPending({ kind: 'mark', leg, outcome: 'succeeded' })}>
                Mark succeeded
              </Button>
              <Button size="sm" onClick={() => setPending({ kind: 'mark', leg, outcome: 'failed' })}>
                Mark failed
              </Button>
            </>
          )}
        </div>
      ),
    },
  ]

  const dialog =
    pending?.kind === 'mark'
      ? {
          title: `Mark ${pending.leg.service} as ${pending.outcome}?`,
          body:
            pending.outcome === 'succeeded'
              ? `This records that ${pending.leg.step} for ${pending.leg.service} finished. Only do this after checking it in AWS.`
              : `This records that ${pending.leg.step} for ${pending.leg.service} did not finish. Only do this after checking it in AWS.`,
          confirmLabel: pending.outcome === 'succeeded' ? 'Mark succeeded' : 'Mark failed',
          danger: false,
        }
      : {
          title: 'Release locks without resolving?',
          body: 'The run will be marked failed and other runs may then touch these services, even though what happened to them is still unknown.',
          confirmLabel: 'Release locks',
          danger: true,
        }

  return (
    <section className="reconcile" aria-label="Unconfirmed outcomes">
      <Note tone="warning">Yumdrake could not confirm what happened to these. Check them in AWS, then say what happened.</Note>
      {error && <Note tone="error">{error}</Note>}
      <DataTable
        label="Services with an unknown outcome"
        columns={columns}
        rows={unknownLegs}
        rowKey={(leg) => leg.id}
        empty={null}
        minWidth={760}
      />
      {me.admin && (
        <div className="reconcile-release">
          <Button variant="danger" size="sm" loading={releaseLocks.isPending} onClick={() => setPending({ kind: 'release' })}>
            Release locks without resolving
          </Button>
        </div>
      )}
      <ConfirmDialog open={pending !== null} {...dialog} onConfirm={confirm} onCancel={() => setPending(null)} />
    </section>
  )
}
