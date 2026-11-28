import { useState, type FormEvent } from 'react'
import { ApiError, errorMessage } from '../../api/client'
import type { Folder, JobDetail } from '../../api/types'
import { Button, Card, Field, Note, Select, TextInput } from '../../ui'
import { useRegisterJob } from './queries'

type FieldName = 'repo' | 'branch' | 'path' | 'folder'

interface AddJobFormProps {
  folders: Folder[]
  /** The folder the job starts in. */
  initialFolderId: number
  onAdded: (job: JobDetail) => void
  onCancel: () => void
}

// The server names the field a validation message is about at the start of the message
// ('repo "x" must look like owner/name'); a message about anything else stays with the form.
function fieldOf(error: ApiError): FieldName | undefined {
  if (error.status !== 400) {
    return undefined
  }
  const first = error.message.trim().split(/[\s_]/)[0]
  return first === 'repo' || first === 'branch' || first === 'path' || first === 'folder' ? first : undefined
}

export function AddJobForm({ folders, initialFolderId, onAdded, onCancel }: AddJobFormProps) {
  const [folderId, setFolderId] = useState(initialFolderId)
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [path, setPath] = useState('')
  const register = useRegisterJob()

  const submit = (event: FormEvent) => {
    event.preventDefault()
    register.mutate({ folder_id: folderId, repo: repo.trim(), branch: branch.trim(), path: path.trim() }, { onSuccess: onAdded })
  }

  const error = register.error
  const conflict = error?.status === 409
  const onField = error ? fieldOf(error) : undefined
  const message = error ? errorMessage(error) : undefined
  const fieldError = (name: FieldName) => (onField === name ? message : undefined)

  return (
    <Card>
      <form className="jobs-form" onSubmit={submit} noValidate>
        <div>
          <h2>Add job</h2>
          <p className="jobs-sub">
            Point Yumdrake at a YAML file in GitHub. It reads and checks the file, builds the job&apos;s page from it, and re-reads it
            whenever the repository changes.
          </p>
        </div>
        <div className="jobs-form-grid">
          <Field label="Folder for the new job" error={fieldError('folder')}>
            <Select value={folderId} onChange={(event) => setFolderId(Number(event.target.value))}>
              {folders.map((folder) => (
                <option key={folder.id} value={folder.id}>
                  {folder.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Repository" hint="Owner and name, for example example-org/search" error={fieldError('repo')}>
            <TextInput value={repo} onChange={(event) => setRepo(event.target.value)} />
          </Field>
          <Field label="Branch" error={fieldError('branch')}>
            <TextInput value={branch} onChange={(event) => setBranch(event.target.value)} />
          </Field>
          <Field label="Path to the job YAML" error={fieldError('path')}>
            <TextInput className="mono" value={path} placeholder="deploy/yumdrake.yaml" onChange={(event) => setPath(event.target.value)} />
          </Field>
        </div>
        {error && !onField && (
          <Note tone="error">{conflict ? 'This file is already registered as a job' : message}</Note>
        )}
        <div className="jobs-actions">
          <Button type="submit" variant="primary" disabled={repo.trim() === '' || path.trim() === ''} loading={register.isPending}>
            Read YAML and add job
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </form>
    </Card>
  )
}
