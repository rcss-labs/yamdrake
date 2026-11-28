import { Link, useParams, useSearchParams } from 'react-router-dom'
import { ApiError } from '../../api/error'
import type { JobDetail } from '../../api/types'
import { useCan } from '../../api/hooks'
import { Button, Card, ErrorState, Loading, PageHeader, Tabs, type TabItem } from '../../ui'
import { DefinitionTab } from './DefinitionTab'
import { HistoryTab } from './HistoryTab'
import { JobFacts } from './JobFacts'
import { RunTab } from './RunTab'
import { useJob } from './jobQueries'
import './job-page.css'

const tabs: TabItem[] = [
  { id: 'history', label: 'History' },
  { id: 'run', label: 'Run' },
  { id: 'definition', label: 'Definition' },
]
const defaultTab = 'definition'
const panelId = (tabId: string) => `job-panel-${tabId}`

function serviceCount(job: JobDetail): number {
  const names = new Set<string>()
  for (const group of job.definition?.groups ?? []) {
    for (const service of group.services ?? []) names.add(service)
  }
  return names.size
}

function approvalText(job: JobDetail): string {
  const labels = job.definition?.approval?.labels ?? []
  return labels.length > 0 ? `Needs approval in ${labels.join(', ')}` : 'No approval needed'
}

export function JobPage() {
  const { id = '' } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const canDo = useCan()
  const query = useJob(id)

  if (query.isPending) return <Loading />
  if (query.isError) {
    if (query.error instanceof ApiError && query.error.status === 404) {
      return (
        <>
          <PageHeader title="Job not found" lead="There is no job at this address. It may have been removed." />
          <Card>
            <Link to="/jobs">Go to Jobs</Link>
          </Card>
        </>
      )
    }
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />
  }

  const job = query.data
  const requested = searchParams.get('tab')
  const active = tabs.some((tab) => tab.id === requested) ? (requested as string) : defaultTab
  const activeLabel = tabs.find((tab) => tab.id === active)?.label ?? ''
  const definition = job.definition
  const count = serviceCount(job)
  const jobId = job.job_id || definition?.id || ''

  return (
    <>
      <PageHeader
        crumbs={
          <>
            <Link to="/jobs">Jobs</Link>
            <span aria-hidden="true">/</span>
            <span>{job.folder}</span>
          </>
        }
        title={job.title || job.path}
        lead={
          (jobId || definition) && (
            <span className="job-meta">
              {jobId && <span className="mono">{jobId}</span>}
              {definition && <span>{`Deploys ${count} ${count === 1 ? 'service' : 'services'}`}</span>}
              {definition && <span>{approvalText(job)}</span>}
            </span>
          )
        }
        actions={
          // The button only opens the Run tab, which says what the person may do there.
          <div className="job-run">
            <Button variant="primary" disabled={!job.runnable} onClick={() => setSearchParams({ tab: 'run' }, { replace: true })}>
              Run this job
            </Button>
            {!job.runnable && <span className="job-sub">This job has no definition that can be run yet</span>}
          </div>
        }
      />
      <JobFacts job={job} />
      <Tabs
        label="Job sections"
        tabs={tabs}
        active={active}
        panelId={panelId}
        onChange={(tab) => setSearchParams({ tab }, { replace: true })}
      />
      <div role="tabpanel" id={panelId(active)} aria-label={activeLabel} className="job-panel">
        {active === 'history' && <HistoryTab job={job} />}
        {active === 'run' && <RunTab job={job} />}
        {active === 'definition' && (
          <DefinitionTab job={job} routeId={id} canSync={canDo('manage_jobs', { folderId: job.folder_id })} />
        )}
      </div>
    </>
  )
}
