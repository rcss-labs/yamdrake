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

type Plain = string | number | boolean

function isPlain(value: unknown): value is Plain {
  return ['string', 'number', 'boolean'].includes(typeof value)
}

// A plain value, or a list of plain values ("none" when empty). Anything else is not shown.
function valueText(value: unknown): string | null {
  if (isPlain(value)) {
    return String(value)
  }
  if (Array.isArray(value) && value.every(isPlain)) {
    return value.length === 0 ? 'none' : value.join(', ')
  }
  return null
}

type Shown = { key: string; text: string }

// Entries worth showing: a value that says something. Empty strings and ids of 0 ("none") are noise;
// false and other numbers (including 0 for a count) are information.
function shownPairs(pairs: [string, unknown][]): Shown[] {
  return pairs.flatMap(([key, value]) => {
    if (value === '' || (value === 0 && key.endsWith('_id'))) {
      return []
    }
    const text = valueText(value)
    return text === null ? [] : [{ key, text }]
  })
}

function PlainPairs({ shown, separator }: { shown: Shown[]; separator: string }) {
  return shown.map(({ key, text }, index) => (
    <span key={key}>
      {index > 0 && separator}
      {key} <span className="mono">{text}</span>
    </span>
  ))
}

// Plain values are listed as "key value". One level of nested objects (the "before" and "after" of a change)
// is listed as "before: key value, key value; after: ...". Deeper data is not worth a table cell.
function Detail({ detail }: { detail: Record<string, unknown> | undefined }) {
  const entries = Object.entries(detail ?? {})
  const plain = shownPairs(entries)
  const nested = entries
    .flatMap(([key, value]) =>
      typeof value === 'object' && value !== null && !Array.isArray(value)
        ? [{ key, shown: shownPairs(Object.entries(value)) }]
        : [],
    )
    .filter(({ shown }) => shown.length > 0)
  if (plain.length === 0 && nested.length === 0) {
    return null
  }
  return (
    <div className="audit-detail">
      <PlainPairs shown={plain} separator=" · " />
      {nested.map(({ key, shown }, index) => (
        <span key={key}>
          {(plain.length > 0 || index > 0) && (index > 0 ? '; ' : ' · ')}
          {key}: <PlainPairs shown={shown} separator=", " />
        </span>
      ))}
    </div>
  )
}

const columns: DataColumn<AuditEntry>[] = [
  { key: 'when', header: 'When', render: (entry) => <span className="audit-nowrap">{formatWhen(entry.at)}</span> },
  { key: 'who', header: 'Who', render: (entry) => <span className="audit-who audit-nowrap">{entry.actor}</span> },
  {
    key: 'what',
    header: 'What',
    render: (entry) => (
      <>
        <span className="mono">{entry.action}</span>
        <Detail detail={entry.detail} />
      </>
    ),
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
