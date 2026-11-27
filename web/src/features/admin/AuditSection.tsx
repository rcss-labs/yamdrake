import { Button, Card, DataTable, EmptyState, ErrorState, Loading, type DataColumn } from '../../ui'
import type { AuditEntry } from '../../api/types'
import { errorMessage } from '../../api/client'
import { useAuditLog } from './auditQueries'
import './audit.css'

function formatWhen(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return iso
  }
  return date.toLocaleString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

// Only plain values are listed; the server never records secrets, but nested data is not worth a table cell.
function detailText(detail: AuditEntry['detail']): string {
  return Object.entries(detail ?? {})
    .filter(([, value]) => ['string', 'number', 'boolean'].includes(typeof value))
    .map(([key, value]) => `${key} ${String(value)}`)
    .join(' · ')
}

const columns: DataColumn<AuditEntry>[] = [
  { key: 'when', header: 'When', render: (entry) => <span className="audit-nowrap">{formatWhen(entry.at)}</span> },
  { key: 'who', header: 'Who', render: (entry) => <span className="audit-who audit-nowrap">{entry.actor}</span> },
  {
    key: 'what',
    header: 'What',
    render: (entry) => {
      const detail = detailText(entry.detail)
      return (
        <>
          <span className="mono">{entry.action}</span>
          {detail && <div className="audit-detail">{detail}</div>}
        </>
      )
    },
  },
  {
    key: 'target',
    header: 'On what',
    render: (entry) => <span className="mono audit-nowrap">{`${entry.target_type} ${entry.target_id}`}</span>,
  },
]

export function AuditSection() {
  const log = useAuditLog()

  if (log.isPending) {
    return <Loading />
  }
  if (log.isError && !log.data) {
    return <ErrorState error={log.error} onRetry={() => void log.refetch()} />
  }
  const entries = log.data?.entries ?? []

  return (
    <Card flush>
      <DataTable
        label="Audit log"
        columns={columns}
        rows={entries}
        rowKey={(entry) => entry.id}
        minWidth={720}
        empty={
          <EmptyState title="Nothing has been recorded yet">
            Sign-ins and changes to access, connections and jobs appear here as they happen.
          </EmptyState>
        }
      />
      {(log.hasNextPage || log.isFetchNextPageError) && (
        <div className="audit-actions">
          <Button loading={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
            Load older
          </Button>
          {log.isFetchNextPageError && (
            <span className="audit-error" role="alert">
              {errorMessage(log.error)}
            </span>
          )}
        </div>
      )}
    </Card>
  )
}
