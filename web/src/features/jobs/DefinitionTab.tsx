import { errorMessage } from '../../api/client'
import type { JobDetail, JobNeed, JobStep } from '../../api/types'
import { Button, Card, CodeBlock, Facts, Note, ProblemList, Tag } from '../../ui'
import { formatAgo, formatDuration, shortCommit } from './jobFormat'
import { useSyncJob } from './jobQueries'

interface DefinitionTabProps {
  job: JobDetail
  routeId: string
  canSync: boolean
}

function needText(need: JobNeed) {
  return need.scope === 'service' ? `After ${need.step}, per service` : `After ${need.step}, the whole step`
}

function stepFacts(step: JobStep): string[] {
  return [
    ...(step.needs ?? []).map(needText),
    ...(step.max_parallel > 0 ? [`Up to ${step.max_parallel} at a time`] : []),
    ...(step.timeout > 0 ? [`Timeout ${formatDuration(step.timeout)}`] : []),
  ]
}

export function DefinitionTab({ job, routeId, canSync }: DefinitionTabProps) {
  const sync = useSyncJob(routeId)
  const definition = job.definition
  const groups = definition?.groups ?? []
  const steps = definition?.steps ?? []
  const groupLabel = (id: string) => groups.find((group) => group.id === id)?.label ?? id

  return (
    <div className="job-definition">
      <div className="job-toolbar">
        <Facts
          className="job-sources"
          items={[
            { label: 'Repository', value: <span className="mono">{job.repo}</span> },
            { label: 'Branch', value: <span className="mono">{job.branch}</span> },
            { label: 'Path', value: <span className="mono">{job.path}</span> },
            { label: 'Commit', value: job.commit ? <span className="mono">{shortCommit(job.commit)}</span> : 'None yet' },
            { label: 'Synced', value: formatAgo(job.synced_at) },
            { label: 'Checked', value: formatAgo(job.checked_at) },
          ]}
        />
        {canSync && (
          <div className="job-sync">
            <Button size="sm" loading={sync.isPending} onClick={() => sync.mutate()}>
              Re-read from GitHub
            </Button>
            {sync.isError && <span className="job-sync-error" role="alert">{errorMessage(sync.error)}</span>}
          </div>
        )}
      </div>
      <p className="job-sub">Edited in git, read-only here.</p>

      {!job.runnable && (
        <Note tone="error">
          This job cannot be run. {job.sync_error || 'No good version of the file has been read yet.'}
        </Note>
      )}
      {job.runnable && job.sync_error !== '' && (
        <Note tone="warning">The last read from GitHub failed, so the stored version is shown. {job.sync_error}</Note>
      )}
      {job.problems.length > 0 && (
        <Note tone="warning" className="job-problems">
          <p>
            {job.commit ? (
              <>
                The last good version (<span className="mono">{shortCommit(job.commit)}</span>) is still in use.
              </>
            ) : (
              <>No good version has been read yet.</>
            )}{' '}
            The newest commit
            {job.problems_commit && (
              <>
                {' '}
                (<span className="mono">{shortCommit(job.problems_commit)}</span>)
              </>
            )}{' '}
            has these problems:
          </p>
          <ProblemList problems={job.problems} />
        </Note>
      )}

      {definition && (
        <div className="job-columns">
          <Card className="job-section">
            <h2>Groups</h2>
            {groups.length === 0 && <p className="job-sub">This job has no groups.</p>}
            <ul className="job-list">
              {groups.map((group) => (
                <li key={group.id} aria-label={group.label}>
                  <div className="job-name">{group.label}</div>
                  <div className="job-services">
                    {(group.services ?? []).map((service) => (
                      <span key={service} className="mono">
                        {service}
                      </span>
                    ))}
                  </div>
                </li>
              ))}
            </ul>
          </Card>
          <Card className="job-section">
            <h2>Steps</h2>
            {steps.length === 0 && <p className="job-sub">This job has no steps.</p>}
            <ul className="job-list">
              {steps.map((step) => (
                <li key={step.name} aria-label={step.name}>
                  <div className="job-step-head">
                    <span className="mono job-name">{step.name}</span>
                    <Tag className="mono">{step.use}</Tag>
                  </div>
                  <div className="job-sub">Group: {groupLabel(step.group)}</div>
                  {stepFacts(step).map((text) => (
                    <div key={text} className="job-sub">
                      {text}
                    </div>
                  ))}
                </li>
              ))}
            </ul>
          </Card>
        </div>
      )}

      {job.source !== '' && <CodeBlock label="Job file">{job.source}</CodeBlock>}
    </div>
  )
}
