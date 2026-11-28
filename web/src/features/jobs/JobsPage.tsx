import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { errorMessage } from '../../api/client'
import type { Folder, JobSummary, RunSummary } from '../../api/types'
import { can, canSomewhere } from '../../auth/permissions'
import { useMe } from '../../auth/session'
import { Button, Card, DataTable, EmptyState, ErrorState, Icon, Loading, PageHeader, SearchBox, Select, StatusPill, Tag, Time } from '../../ui'
import type { DataColumn } from '../../ui'
import { AddJobForm } from './AddJobForm'
import { NewFolderForm } from './NewFolderForm'
import { useRuns } from '../runs/queries'
import { useFolders, useJobs, useMoveJob } from './queries'
import './jobs.css'

type Row =
  | { kind: 'group'; folder: Folder; count: number }
  | { kind: 'job'; job: JobSummary }
  | { kind: 'empty'; folder: Folder }

const ALL_FOLDERS = 'all'

const plural = (count: number, one: string, many: string) => `${count} ${count === 1 ? one : many}`

function Deploys({ job }: { job: JobSummary }) {
  return <>{plural(job.services, 'service', 'services')}</>
}

function Approval({ job }: { job: JobSummary }) {
  if (job.approval_labels.length === 0) {
    return <>None</>
  }
  return (
    <span className="jobs-tags">
      {job.approval_labels.map((label) => (
        <Tag key={label}>{label}</Tag>
      ))}
    </span>
  )
}

function State({ job }: { job: JobSummary }) {
  if (job.runnable && job.problem_count === 0) {
    return <span className="jobs-quiet">Runnable</span>
  }
  // Problems are a warning, not a failure: a job with problems may still run its last good version.
  return <StatusPill status="unknown" label={job.problem_count > 0 ? `Has problems (${job.problem_count})` : 'Has problems'} />
}

// The most the server returns for one request.
const LAST_RUNS_LIMIT = 100

function LastRun({ run, runsQuery }: { run: RunSummary | undefined; runsQuery: ReturnType<typeof useRuns> }) {
  if (run) {
    return (
      <>
        <StatusPill status={run.state} />
        <div className="jobs-quiet">
          <Time iso={run.started_at ?? run.created_at} />
        </div>
      </>
    )
  }
  if (runsQuery.isError) return <span className="jobs-quiet">Not available</span>
  if (!runsQuery.data) return <span className="jobs-quiet">—</span>
  // A full page means older runs exist that this page did not reach.
  return <span className="jobs-quiet">{runsQuery.data.next_before === null ? 'No runs yet' : 'No recent runs'}</span>
}

export function JobsPage() {
  const me = useMe()
  const navigate = useNavigate()
  const jobsQuery = useJobs()
  const foldersQuery = useFolders()
  const move = useMoveJob()
  const [search, setSearch] = useState('')
  const [folderFilter, setFolderFilter] = useState(ALL_FOLDERS)
  const [openForm, setOpenForm] = useState<'job' | 'folder' | null>(null)
  const [moveFailures, setMoveFailures] = useState<Record<number, string>>({})

  // One request for the newest runs of every job; the list is newest first, so the first run seen per job is its last.
  const runsQuery = useRuns({ limit: LAST_RUNS_LIMIT })
  const newestRunByJob = new Map<number, RunSummary>()
  for (const run of runsQuery.data?.runs ?? []) {
    if (!newestRunByJob.has(run.job_source_id)) newestRunByJob.set(run.job_source_id, run)
  }

  const allJobs = jobsQuery.data?.jobs
  const folders = foldersQuery.data?.folders
  const canManage = canSomewhere(me, 'manage_jobs')

  const retry = () => {
    void jobsQuery.refetch()
    void foldersQuery.refetch()
  }

  const header = (lead?: string) => (
    <PageHeader
      title="Jobs"
      lead={lead}
      actions={
        canManage && folders ? (
          <>
            <Button onClick={() => setOpenForm('folder')}>New folder</Button>
            <Button variant="primary" onClick={() => setOpenForm('job')} disabled={folders.length === 0}>
              Add job
            </Button>
          </>
        ) : undefined
      }
    />
  )

  const failure = jobsQuery.error ?? foldersQuery.error
  if (failure) {
    return (
      <>
        {header()}
        <ErrorState error={failure} onRetry={retry} />
      </>
    )
  }
  if (!allJobs || !folders) {
    return (
      <>
        {header()}
        <Loading />
      </>
    )
  }

  const query = search.trim().toLowerCase()
  const folderId = folderFilter === ALL_FOLDERS ? undefined : Number(folderFilter)
  const matches = (job: JobSummary) =>
    (folderId === undefined || job.folder_id === folderId) &&
    (query === '' || `${job.job_id} ${job.title} ${job.folder} ${job.path}`.toLowerCase().includes(query))
  const shown = allJobs.filter(matches)

  const rows: Row[] = []
  for (const folder of folders) {
    if (folderId !== undefined && folder.id !== folderId) {
      continue
    }
    const inFolder = shown.filter((job) => job.folder_id === folder.id)
    if (query !== '' && inFolder.length === 0) {
      continue
    }
    rows.push({ kind: 'group', folder, count: inFolder.length })
    rows.push(...inFolder.map((job): Row => ({ kind: 'job', job })))
    if (inFolder.length === 0) {
      rows.push({ kind: 'empty', folder })
    }
  }

  const moveJob = (job: JobSummary, folder: Folder) => {
    setMoveFailures((current) => {
      const rest = { ...current }
      delete rest[job.id]
      return rest
    })
    move.mutate(
      { job, folder },
      {
        onError: (error) => setMoveFailures((current) => ({ ...current, [job.id]: errorMessage(error) })),
      },
    )
  }

  const columns: DataColumn<Row>[] = [
    {
      key: 'job',
      header: 'Job',
      render: (row) => {
        if (row.kind === 'group') {
          return (
            <div className="jobs-group">
              <Icon name="folder" />
              <span>{row.folder.name}</span>
              <span className="jobs-count">{plural(row.count, 'job', 'jobs')}</span>
            </div>
          )
        }
        if (row.kind === 'empty') {
          return <span className="jobs-quiet">No jobs in this folder yet</span>
        }
        const { job } = row
        return (
          <>
            <Link to={`/jobs/${job.id}`}>{job.title || job.path}</Link>
            <div className="mono jobs-quiet">{job.job_id || `${job.repo} on ${job.branch}`}</div>
          </>
        )
      },
    },
    { key: 'deploys', header: 'Deploys', render: (row) => (row.kind === 'job' ? <Deploys job={row.job} /> : null) },
    { key: 'approval', header: 'Approval', render: (row) => (row.kind === 'job' ? <Approval job={row.job} /> : null) },
    { key: 'state', header: 'State', render: (row) => (row.kind === 'job' ? <State job={row.job} /> : null) },
    { key: 'last', header: 'Last run', render: (row) => (row.kind === 'job' ? <LastRun run={newestRunByJob.get(row.job.id)} runsQuery={runsQuery} /> : null) },
  ]
  if (canManage) {
    columns.push({
      key: 'folder',
      header: 'Folder',
      render: (row) => {
        if (row.kind !== 'job') {
          return null
        }
        const { job } = row
        if (!can(me, 'manage_jobs', { folderId: job.folder_id })) {
          return null
        }
        return (
          <>
            <Select
              size="sm"
              aria-label={`Folder for ${job.title || job.path}`}
              value={job.folder_id}
              onChange={(event) => {
                const target = folders.find((folder) => folder.id === Number(event.target.value))
                if (target) {
                  moveJob(job, target)
                }
              }}
            >
              {folders.map((folder) => (
                <option key={folder.id} value={folder.id}>
                  {folder.name}
                </option>
              ))}
            </Select>
            {moveFailures[job.id] && (
              <p className="jobs-move-error" role="alert">
                We could not move this job. {moveFailures[job.id]}
              </p>
            )}
          </>
        )
      },
    })
  }

  const lead = `${plural(allJobs.length, 'job', 'jobs')} in ${plural(folders.length, 'folder', 'folders')}. Each job has its own page, built from its YAML.`

  if (allJobs.length === 0 && folders.length === 0) {
    return (
      <>
        {header()}
        {canManage && openForm === 'folder' && (
          <NewFolderForm
            onCreated={(folder) => {
              setFolderFilter(String(folder.id))
              setOpenForm(null)
            }}
            onCancel={() => setOpenForm(null)}
          />
        )}
        <EmptyState title="No jobs yet">
          {canManage ? 'Create a folder, then add a job from a YAML file in GitHub.' : 'Ask someone who manages jobs to add the first one.'}
        </EmptyState>
      </>
    )
  }

  return (
    <>
      {header(lead)}
      {canManage && openForm === 'folder' && (
        <NewFolderForm
          onCreated={(folder) => {
            setSearch('')
            setFolderFilter(String(folder.id))
            setOpenForm(null)
          }}
          onCancel={() => setOpenForm(null)}
        />
      )}
      {canManage && openForm === 'job' && (
        <AddJobForm
          folders={folders}
          initialFolderId={folderId ?? folders[0]?.id ?? 0}
          onAdded={(job) => navigate(`/jobs/${job.id}`)}
          onCancel={() => setOpenForm(null)}
        />
      )}
      <div className="jobs-toolbar">
        <SearchBox label="Find a job" value={search} onChange={setSearch} />
        <Select aria-label="Folder" value={folderFilter} onChange={(event) => setFolderFilter(event.target.value)}>
          <option value={ALL_FOLDERS}>All folders</option>
          {folders.map((folder) => (
            <option key={folder.id} value={folder.id}>
              {folder.name}
            </option>
          ))}
        </Select>
      </div>
      <Card flush>
        <DataTable
          label="Jobs"
          columns={columns}
          rows={rows}
          minWidth={880}
          rowKey={(row) => (row.kind === 'job' ? `job-${row.job.id}` : `${row.kind}-${row.folder.id}`)}
          rowKind={(row) => (row.kind === 'group' ? 'group' : undefined)}
          empty={
            <div className="jobs-no-match">
              <p>No job matches your search</p>
              <p className="jobs-quiet">Clear the search or pick another folder to see more jobs.</p>
            </div>
          }
        />
      </Card>
    </>
  )
}
