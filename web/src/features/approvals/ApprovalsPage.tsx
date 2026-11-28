import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { errorMessage } from '../../api/client'
import type { ApprovalItem } from '../../api/types'
import { Button, Card, ConfirmDialog, ErrorState, Loading, Note, PageHeader, Tag, Time } from '../../ui'
import { approvalsKey, useApprovals } from '../runs/queries'
import { useRejectRun } from './approvalsActions'
import './approvals.css'

function ApprovalCard({ item, onReject }: { item: ApprovalItem; onReject: (item: ApprovalItem) => void }) {
  const titleId = `approval-${item.run_id}`
  const versions = item.services.map((service) => `${service.service} ${service.version}`).join(', ')
  return (
    <Card className="approvals-card" role="article" aria-labelledby={titleId}>
      <div className="approvals-text">
        <div id={titleId} className="approvals-title">
          {item.requested_by_name} wants to run {item.job_name} in <span className="mono">{item.environment}</span> <Tag>{item.label}</Tag>
        </div>
        <div className="approvals-sub">
          {versions ? `${versions}. ` : ''}Requested <Time iso={item.requested_at} />. Expires <Time iso={item.expires_at} />.
        </div>
        <div className="approvals-sub">{item.can_approve ? 'You can approve this run.' : item.cannot_approve_reason}</div>
      </div>
      <div className="approvals-actions">
        <Link className="ui-btn ui-btn-default" to={`/runs/${item.run_id}`}>
          Details
        </Link>
        {item.can_approve && (
          <>
            {/* Approving needs the plan on screen, so this goes to the run page rather than approving here. */}
            <Link className="ui-btn ui-btn-primary" to={`/runs/${item.run_id}`}>
              Review and approve
            </Link>
            <Button variant="danger" onClick={() => onReject(item)}>
              Reject
            </Button>
          </>
        )}
      </div>
    </Card>
  )
}

export function ApprovalsPage() {
  const approvals = useApprovals()
  const reject = useRejectRun()
  const queryClient = useQueryClient()
  const [rejecting, setRejecting] = useState<ApprovalItem | null>(null)
  const [failure, setFailure] = useState('')

  async function confirmReject() {
    const item = rejecting
    if (!item) return
    setRejecting(null)
    setFailure('')
    try {
      await reject.mutateAsync({ runId: item.run_id })
    } catch (error) {
      setFailure(errorMessage(error))
      // The run may have been decided meanwhile: show the current list, not the stale card.
      await queryClient.invalidateQueries({ queryKey: approvalsKey })
    } finally {
      reject.reset()
    }
  }

  return (
    <>
      <PageHeader title="Approvals" lead="Runs waiting for a second person. Only jobs that ask for approval appear here." />
      {failure && <Note tone="error">{failure}</Note>}
      {approvals.isPending && <Loading />}
      {approvals.error && <ErrorState error={approvals.error} onRetry={() => void approvals.refetch()} />}
      {approvals.data && approvals.data.approvals.length === 0 && <Card className="approvals-empty">Nothing is waiting for approval</Card>}
      {approvals.data?.approvals.map((item) => (
        <ApprovalCard key={item.run_id} item={item} onReject={setRejecting} />
      ))}
      <ConfirmDialog
        open={rejecting !== null}
        title={`Reject run #${rejecting?.run_id ?? ''}?`}
        body="The run will be rejected and will not start. Nothing has been deployed."
        confirmLabel="Reject run"
        danger
        onConfirm={() => void confirmReject()}
        onCancel={() => setRejecting(null)}
      />
    </>
  )
}
