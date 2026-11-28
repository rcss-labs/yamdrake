import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import type { Folder } from '../../api/types'
import { Button, Card, Field, TextInput } from '../../ui'
import { useCreateFolder } from './queries'

interface NewFolderFormProps {
  onCreated: (folder: Folder) => void
  onCancel: () => void
}

export function NewFolderForm({ onCreated, onCancel }: NewFolderFormProps) {
  const [name, setName] = useState('')
  const create = useCreateFolder()

  const submit = (event: FormEvent) => {
    event.preventDefault()
    create.mutate({ name: name.trim() }, { onSuccess: onCreated })
  }

  return (
    <Card>
      <form className="jobs-form" onSubmit={submit} noValidate>
        <div>
          <h2>New folder</h2>
          <p className="jobs-sub">Folders only organise this page. They do not change what a job does or who may run it.</p>
        </div>
        <Field label="Folder name" error={create.error ? errorMessage(create.error) : undefined} className="jobs-narrow">
          <TextInput value={name} onChange={(event) => setName(event.target.value)} />
        </Field>
        <div className="jobs-actions">
          <Button type="submit" variant="primary" disabled={name.trim() === ''} loading={create.isPending}>
            Create folder
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </form>
    </Card>
  )
}
