import { Link, useSearchParams } from 'react-router-dom'
import { errorMessage } from '../../api/client'
import { useCan } from '../../api/hooks'
import type { Inventory } from '../../api/types'
import { Button, EmptyState, ErrorState, Facts, Loading, Note, PageHeader, ProblemList, Tabs } from '../../ui'
import { EnvironmentsTab } from './EnvironmentsTab'
import { ServicesTab } from './ServicesTab'
import { useInventory, useSyncInventory } from './queries'
import './inventory.css'

type TabId = 'environments' | 'services'

const tabs = [
  { id: 'environments', label: 'Environments' },
  { id: 'services', label: 'Services' },
]

const panelId = (tabId: string) => `inventory-panel-${tabId}`

const shortCommit = (commit: string) => commit.slice(0, 7)

function formatTime(iso: string | null): string {
  if (!iso) return 'never'
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'short' })
}

function Commit({ commit }: { commit: string }) {
  return (
    <span className="mono inv-commit" title={commit}>
      {shortCommit(commit)}
    </span>
  )
}

function RejectedNote({ rejected }: { rejected: NonNullable<Inventory['rejected']> }) {
  return (
    <Note tone="warning" className="inv-note">
      <p>
        A newer commit, <Commit commit={rejected.commit} />, was rejected. What you see is the last good inventory, which stays in
        use until the problems below are fixed in git.
      </p>
      <ProblemList problems={rejected.problems} />
    </Note>
  )
}

export function InventoryPage() {
  const can = useCan()
  const canManage = can('manage_connections')
  const [params, setParams] = useSearchParams()
  const inventory = useInventory()
  const sync = useSyncInventory()
  const active: TabId = params.get('tab') === 'services' ? 'services' : 'environments'

  function selectTab(id: string) {
    const next = new URLSearchParams(params)
    next.set('tab', id)
    setParams(next)
  }

  const actions = canManage ? (
    <div className="inv-sync">
      {sync.isError && <span className="inv-sync-error">{errorMessage(sync.error)}</span>}
      <Button loading={sync.isPending} onClick={() => sync.mutate()}>
        Sync from git
      </Button>
    </div>
  ) : undefined

  const header = (
    <PageHeader
      title="Inventory"
      lead="Defined in YAML in git, read-only here. Environments and services come from the active commit."
      actions={actions}
    />
  )

  if (inventory.isPending) {
    return (
      <>
        {header}
        <Loading />
      </>
    )
  }
  if (inventory.isError) {
    return (
      <>
        {header}
        <ErrorState error={inventory.error} onRetry={() => void inventory.refetch()} />
      </>
    )
  }

  const data = inventory.data
  if (data.commit === '') {
    return (
      <>
        {header}
        <EmptyState
          title="No inventory source is set"
          action={
            canManage ? (
              <Link className="inv-link" to="/admin/sources">
                Set the inventory source
              </Link>
            ) : undefined
          }
        >
          {canManage
            ? 'Point Yumdrake at the git repository that holds the environments and services, then sync.'
            : 'Ask someone who manages connections to set the git repository the inventory is read from.'}
        </EmptyState>
      </>
    )
  }

  return (
    <>
      {header}
      <Facts
        className="inv-facts"
        items={[
          { label: 'Active commit', value: <Commit commit={data.commit} /> },
          { label: 'Activated', value: formatTime(data.activated_at) },
          { label: 'Last checked', value: formatTime(data.checked_at) },
        ]}
      />
      {data.sync_error !== '' && (
        <Note tone="warning" className="inv-note">
          The last check on {formatTime(data.sync_error_at)} failed: <span className="mono">{data.sync_error}</span>
        </Note>
      )}
      {data.rejected && <RejectedNote rejected={data.rejected} />}
      <Tabs label="Inventory sections" tabs={tabs} active={active} onChange={selectTab} panelId={panelId} />
      <div role="tabpanel" id={panelId(active)} className="inv-panel">
        {active === 'environments' ? <EnvironmentsTab inventory={data} /> : <ServicesTab inventory={data} />}
      </div>
    </>
  )
}
