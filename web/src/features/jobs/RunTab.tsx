import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { errorMessage } from '../../api/client'
import { useCan } from '../../api/hooks'
import type { CreateRunRequest, EnvironmentSummary, GroupVersions, JobDetail, JobGroup, JobVersions, Selection } from '../../api/types'
import { Button, Card, Checkbox, ErrorState, Field, Loading, Note, ProblemList, Select } from '../../ui'
import { FreezeBanner } from '../freezes/FreezeBanner'
import { useInventory } from '../inventory/queries'
import { useJobVersions } from '../runs/queries'
import { useCreateRun } from './runQueries'
import './run-tab.css'

const plural = (count: number) => (count === 1 ? 'service' : 'services')

/** What the person has chosen so far, beyond the defaults (every available service, the newest common version). */
interface Choices {
  /** `group/service` keys of services the person unticked. */
  unticked: Record<string, true>
  /** group id -> chosen version; absent means the newest common one. */
  versions: Record<string, string>
  /** `group/service` -> version override; absent means the group's version. */
  overrides: Record<string, string>
}

const serviceKey = (group: JobGroup, service: string) => `${group.id}/${service}`

/** Services of a group whose versions could be read in this environment, in the job's order. */
function availableServices(group: JobGroup, versions: GroupVersions | undefined): string[] {
  return (group.services ?? []).filter((service) => versions !== undefined && service in versions.services && !(service in versions.unavailable))
}

function unavailableReason(service: string, versions: GroupVersions | undefined): string {
  return versions?.unavailable[service] ?? 'its versions could not be read'
}

function groupVersion(group: JobGroup, versions: GroupVersions | undefined, choices: Choices): string {
  return choices.versions[group.id] ?? versions?.common[0] ?? ''
}

/** The create body for the current choices. Groups with nothing selected are left out. */
function buildSelection(groups: JobGroup[], data: JobVersions, choices: Choices) {
  const selection: Selection = {}
  const lines: { group: JobGroup; version: string; count: number; overrides: [string, string][] }[] = []
  let missingVersion = false
  for (const group of groups) {
    const versions = data.groups[group.id]
    const selected = availableServices(group, versions).filter((service) => !choices.unticked[serviceKey(group, service)])
    if (selected.length === 0) continue
    const version = groupVersion(group, versions, choices)
    if (version === '') missingVersion = true
    const effective = (service: string) => choices.overrides[serviceKey(group, service)] || version
    const overrides = selected.filter((service) => effective(service) !== version).map((service): [string, string] => [service, effective(service)])
    // The engine selects only the services a group names (an absent `services` selects none), so every selected service is named.
    selection[group.id] = { version, services: Object.fromEntries(selected.map((service) => [service, effective(service)])) }
    lines.push({ group, version, count: selected.length, overrides })
  }
  return { selection, lines, missingVersion }
}

interface RunFormProps {
  job: JobDetail
  groups: JobGroup[]
  environment: EnvironmentSummary
  environments: EnvironmentSummary[]
  onEnvironment(name: string): void
}

function RunForm({ job, groups, environment, environments, onEnvironment }: RunFormProps) {
  const canDo = useCan()
  const navigate = useNavigate()
  const versionsQuery = useJobVersions(job.id, environment.name)
  const create = useCreateRun(job.id)
  const [choices, setChoices] = useState<Choices>({ unticked: {}, versions: {}, overrides: {} })
  const [open, setOpen] = useState<Record<string, boolean>>({})

  const allowed = canDo('run', { label: environment.label, environment: environment.name, folderId: job.folder_id })
  const approvalNeeded = (job.definition?.approval?.labels ?? []).includes(environment.label)

  // Any change to the choices clears the last answer of the server, which was about the old choices.
  const change = (update: (current: Choices) => Choices) => {
    create.reset()
    setChoices(update)
  }

  const environmentCard = (
    <Card className="run-card">
      <h2>Environment</h2>
      <Field label="Environment">
        <Select className="run-env" value={environment.name} onChange={(event) => onEnvironment(event.target.value)}>
          {environments.map((candidate) => (
            <option key={candidate.name} value={candidate.name}>{`${candidate.name} (${candidate.label})`}</option>
          ))}
        </Select>
      </Field>
      <p className="job-sub">
        One environment for the whole run. {environment.name} is labelled {environment.label} and lives in AWS account{' '}
        <span className="mono">{environment.aws_account}</span>.
      </p>
      {!allowed && <Note>Your role may not run jobs in {environment.label}</Note>}
    </Card>
  )

  if (versionsQuery.isPending) {
    return (
      <div className="run-layout">
        <div className="run-main">{environmentCard}<Loading /></div>
      </div>
    )
  }
  if (versionsQuery.isError) {
    return (
      <div className="run-layout">
        <div className="run-main">
          {environmentCard}
          <ErrorState error={versionsQuery.error} onRetry={() => void versionsQuery.refetch()} />
        </div>
      </div>
    )
  }

  const data = versionsQuery.data
  const { selection, lines, missingVersion } = buildSelection(groups, data, choices)
  const total = lines.reduce((sum, line) => sum + line.count, 0)
  const canPlan = allowed && total > 0 && !missingVersion && !create.isPending

  const plan = () => {
    const request: CreateRunRequest = { environment: environment.name, selection, start: false }
    create.mutate(request, { onSuccess: (run) => navigate(`/runs/${run.id}`) })
  }

  const failure = create.error
  const problems = failure?.problems ?? []

  return (
    <div className="run-layout">
      <div className="run-main">
        {environmentCard}
        {groups.map((group) => {
          const versions = data.groups[group.id]
          const members = group.services ?? []
          const available = availableServices(group, versions)
          const selected = available.filter((service) => !choices.unticked[serviceKey(group, service)])
          const version = groupVersion(group, versions, choices)
          const common = versions?.common ?? []
          const expanded = open[group.id] === true
          return (
            <Card key={group.id} className="run-card">
              <div className="run-group-head">
                <Checkbox
                  label={
                    <>
                      <span className="run-group-name">{group.label}</span>
                      <span className="job-sub">{`${selected.length} of ${members.length} selected`}</span>
                    </>
                  }
                  checked={available.length > 0 && selected.length === available.length}
                  disabled={!allowed || available.length === 0}
                  onChange={() =>
                    change((current) => {
                      const unticked = { ...current.unticked }
                      for (const service of available) {
                        if (selected.length === available.length) unticked[serviceKey(group, service)] = true
                        else delete unticked[serviceKey(group, service)]
                      }
                      return { ...current, unticked }
                    })
                  }
                />
                <Field label={`${group.label} version`} className="run-version">
                  <Select
                    className="mono"
                    value={version}
                    disabled={!allowed || common.length === 0}
                    onChange={(event) => change((current) => ({ ...current, versions: { ...current.versions, [group.id]: event.target.value } }))}
                  >
                    {common.length === 0 && <option value="">No common version</option>}
                    {common.map((tag, index) => (
                      <option key={tag} value={tag}>{index === 0 ? `${tag} (newest)` : tag}</option>
                    ))}
                  </Select>
                </Field>
              </div>
              {members.length > 1 && (
                <div>
                  <Button size="sm" aria-expanded={expanded} onClick={() => setOpen((current) => ({ ...current, [group.id]: !expanded }))}>
                    {expanded ? 'Hide services' : 'Choose services'}
                  </Button>
                </div>
              )}
              {members.length > 1 && expanded && (
                <ul className="run-services">
                  {members.map((service) => {
                    const key = serviceKey(group, service)
                    const isAvailable = available.includes(service)
                    const reasonId = `run-reason-${group.id}-${service}`
                    const tags = (versions?.services[service] ?? []).map((item) => item.tag)
                    return (
                      <li key={service}>
                        <Checkbox
                          label={<span className="mono">{service}</span>}
                          checked={isAvailable && !choices.unticked[key]}
                          disabled={!allowed || !isAvailable}
                          aria-describedby={isAvailable ? undefined : reasonId}
                          onChange={() =>
                            change((current) => {
                              const unticked = { ...current.unticked }
                              if (unticked[key]) delete unticked[key]
                              else unticked[key] = true
                              return { ...current, unticked }
                            })
                          }
                        />
                        {isAvailable ? (
                          <Select
                            size="sm"
                            className="mono"
                            aria-label={`Version for ${service}`}
                            value={choices.overrides[key] ?? ''}
                            disabled={!allowed}
                            onChange={(event) => change((current) => ({ ...current, overrides: { ...current.overrides, [key]: event.target.value } }))}
                          >
                            <option value="">{version === '' ? 'Same as the group' : `Same as the group (${version})`}</option>
                            {tags.map((tag) => (
                              <option key={tag} value={tag}>{tag}</option>
                            ))}
                          </Select>
                        ) : (
                          <span id={reasonId} className="job-sub">{`Not available: ${unavailableReason(service, versions)}`}</span>
                        )}
                      </li>
                    )
                  })}
                </ul>
              )}
              {members.length === 1 && available.length === 0 && (
                <p className="job-sub">{`Not available: ${unavailableReason(members[0] ?? '', versions)}`}</p>
              )}
            </Card>
          )
        })}
        <p className="job-sub">A group's version list shows the tags every service of the group has.</p>
      </div>

      <Card className="run-card run-summary">
        <h2>{`Plan for ${environment.name}`}</h2>
        <FreezeBanner environment={environment.name} label={environment.label} />
        {total === 0 ? (
          <p className="job-sub">Nothing is selected yet</p>
        ) : (
          <>
            <p>{`${total} ${plural(total)} selected`}</p>
            <ol className="run-plan-list">
              {lines.map((line) => (
                <li key={line.group.id}>
                  {`${line.group.label}: ${line.count} ${plural(line.count)} at ${line.version || 'no version'}`}
                  {line.overrides.map(([service, tag]) => (
                    <span key={service} className="run-override">{`${service} at ${tag}`}</span>
                  ))}
                </li>
              ))}
            </ol>
          </>
        )}
        {missingVersion && <Note tone="warning">A selected group has no version that all its services share, so it cannot be planned.</Note>}
        {approvalNeeded && (
          <Note tone="warning">
            This job needs approval in {environment.label}. Once started, the run waits until someone approves it.
          </Note>
        )}
        {failure &&
          (problems.length > 0 ? (
            <Note tone="error">
              <p>The selection cannot be planned.</p>
              <ProblemList problems={problems} />
            </Note>
          ) : (
            <Note tone="error">{errorMessage(failure)}</Note>
          ))}
        {allowed && (
          <Button variant="primary" disabled={!canPlan} loading={create.isPending} onClick={plan}>
            Plan this run
          </Button>
        )}
      </Card>
    </div>
  )
}

export function RunTab({ job }: { job: JobDetail }) {
  const [searchParams, setSearchParams] = useSearchParams()
  const inventory = useInventory()
  const definition = job.definition

  if (!job.runnable || !definition) {
    return (
      <Note tone="error">
        This job cannot be run. {job.sync_error || 'No good version of the file has been read yet.'}
      </Note>
    )
  }
  if (inventory.isPending) return <Loading />
  if (inventory.isError) return <ErrorState error={inventory.error} onRetry={() => void inventory.refetch()} />

  const labels = definition.labels ?? []
  const environments = inventory.data.environments.filter((environment) => labels.includes(environment.label))
  const requested = searchParams.get('env')
  const environment = environments.find((candidate) => candidate.name === requested) ?? environments[0]
  if (!environment) {
    return <Note>No environment in the inventory has a label this job permits.</Note>
  }

  const chooseEnvironment = (name: string) =>
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        next.set('tab', 'run')
        next.set('env', name)
        return next
      },
      { replace: true },
    )

  return (
    <RunForm
      key={environment.name}
      job={job}
      groups={definition.groups ?? []}
      environment={environment}
      environments={environments}
      onEnvironment={chooseEnvironment}
    />
  )
}
