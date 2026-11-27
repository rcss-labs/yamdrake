import { useState } from 'react'
import type { FormEvent } from 'react'
import type { Inventory, InventorySource, JobSummary } from '../../api/types'
import { Button, Card, DataTable, EmptyState, ErrorState, Field, Loading, Note, ProblemList, TextInput } from '../../ui'
import type { DataColumn } from '../../ui'
import { fieldForMessage, useInventory, useInventorySource, useJobSources, useSaveInventorySource } from './connectionsQueries'
import './connections.css'

const sourceFields = ['repo', 'branch', 'path'] as const

/** What the save found when it read the repository again. */
function SaveResult({ result }: { result: Inventory }) {
  if (result.sync_error !== '') {
    return <Note tone="error">{`Saved, but the repository could not be read: ${result.sync_error}. The previous inventory stays in use.`}</Note>
  }
  if (result.rejected) {
    return (
      <Note tone="warning">
        <div className="conn-saved">
          <p>{`Saved, but commit ${result.rejected.commit.slice(0, 12)} has problems, so the last good version stays in use.`}</p>
          <ProblemList problems={result.rejected.problems} />
        </div>
      </Note>
    )
  }
  return (
    <Note>
      {result.commit === ''
        ? 'Saved. Yumdrake re-read the repository.'
        : `Saved. Yumdrake re-read the repository; the active commit is ${result.commit.slice(0, 12)}.`}
    </Note>
  )
}

function InventorySourceForm({ stored }: { stored: InventorySource }) {
  const inventory = useInventory()
  const save = useSaveInventorySource()
  const [repo, setRepo] = useState(stored.repo)
  const [branch, setBranch] = useState(stored.branch)
  const [path, setPath] = useState(stored.path)

  const error = save.error
  const failedField = error ? fieldForMessage(error.message, sourceFields) : undefined
  const messageFor = (field: (typeof sourceFields)[number]) => (error && failedField === field ? error.message : undefined)

  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate({ repo: repo.trim(), branch: branch.trim(), path: path.trim() })
  }

  return (
    <Card>
      <form className="conn-form" onSubmit={submit} noValidate>
        <div>
          <h2>Inventory source</h2>
          <p className="conn-hint">
            Yumdrake reads <span className="mono">environments.yaml</span> and <span className="mono">services/*.yaml</span> under this path.
            Saving replaces the current inventory source and reads it again.
          </p>
        </div>
        {inventory.data && (
          <p className="conn-hint">
            {inventory.data.commit === ''
              ? 'No inventory commit is active yet.'
              : `Active inventory commit: ${inventory.data.commit.slice(0, 12)}`}
          </p>
        )}
        <div className="conn-grid">
          <Field label="Repository" error={messageFor('repo')}>
            <TextInput className="mono" placeholder="example-org/deploy-config" autoComplete="off" value={repo} onChange={(event) => setRepo(event.target.value)} />
          </Field>
          <Field label="Branch" error={messageFor('branch')}>
            <TextInput className="mono" placeholder="main" autoComplete="off" value={branch} onChange={(event) => setBranch(event.target.value)} />
          </Field>
          <Field label="Path" error={messageFor('path')}>
            <TextInput className="mono" placeholder="inventory" autoComplete="off" value={path} onChange={(event) => setPath(event.target.value)} />
          </Field>
        </div>
        {error && !failedField && (
          <p className="conn-error" role="alert">
            {error.message}
          </p>
        )}
        <div className="conn-actions">
          <Button type="submit" variant="primary" loading={save.isPending}>
            Save and re-read
          </Button>
        </div>
        {save.data && <SaveResult result={save.data} />}
      </form>
    </Card>
  )
}

function StoredInventorySource() {
  const stored = useInventorySource()
  if (stored.isPending) {
    return (
      <Card>
        <Loading />
      </Card>
    )
  }
  if (stored.isError) {
    return (
      <Card>
        <ErrorState error={stored.error} onRetry={() => void stored.refetch()} />
      </Card>
    )
  }
  return <InventorySourceForm stored={stored.data} />
}

const jobColumns: DataColumn<JobSummary>[] = [
  { key: 'title', header: 'Job', render: (job) => job.title },
  { key: 'repo', header: 'Repository', render: (job) => <span className="mono">{job.repo}</span> },
  { key: 'branch', header: 'Branch', render: (job) => <span className="mono">{job.branch}</span> },
  { key: 'path', header: 'Path', render: (job) => <span className="mono">{job.path}</span> },
]

function JobSources() {
  const jobs = useJobSources()
  return (
    <Card flush>
      <div className="conn-title">
        <h2>Job sources</h2>
        <p>Each job points at its own YAML file. Add jobs from the Jobs page.</p>
      </div>
      {jobs.isPending && <Loading />}
      {jobs.error && <ErrorState error={jobs.error} onRetry={() => void jobs.refetch()} />}
      {jobs.data && (
        <DataTable
          label="Job sources"
          columns={jobColumns}
          rows={jobs.data.jobs}
          rowKey={(job) => job.id}
          minWidth={640}
          empty={<EmptyState title="No jobs have a source yet">Register a job on the Jobs page and its source appears here.</EmptyState>}
        />
      )}
    </Card>
  )
}

export function SourcesSection() {
  return (
    <>
      <StoredInventorySource />
      <JobSources />
    </>
  )
}
