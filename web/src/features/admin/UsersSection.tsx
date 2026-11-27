import { useEffect, useRef, useState, type FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import type { AdminUser, UserSource } from '../../api/types'
import { Button, Card, Checkbox, DataTable, EmptyState, ErrorState, Field, Loading, Note, Select, StatusPill, Tag, TextInput, type DataColumn } from '../../ui'
import { changeConflictMessage } from './messages'
import { useCreateUser, useGroups, useUpdateUser, useUsers } from './usersQueries'
import './users.css'

const sourceWords: Record<UserSource, string> = {
  local: 'Local',
  saml: 'Single sign-on',
  service: 'Service',
}

const MIN_PASSWORD_LENGTH = 12

function formatLastSignIn(value: string | null) {
  if (value === null) {
    return 'Never'
  }
  return new Date(value).toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'short' })
}

export function UsersSection() {
  const users = useUsers()
  const [editingId, setEditingId] = useState<number | null>(null)

  if (users.isPending) {
    return <Loading />
  }
  if (users.isError) {
    return <ErrorState error={users.error} onRetry={() => void users.refetch()} />
  }
  const rows = users.data.users
  const editing = rows.find((candidate) => candidate.id === editingId)

  return (
    <div className="users-section">
      <UsersTable users={rows} onEditGroups={setEditingId} />
      {editing && <GroupEditor key={editing.id} user={editing} onClose={() => setEditingId(null)} />}
      <CreateUserForm />
    </div>
  )
}

function UsersTable({ users, onEditGroups }: { users: AdminUser[]; onEditGroups: (id: number) => void }) {
  const [confirmingId, setConfirmingId] = useState<number | null>(null)
  const update = useUpdateUser()
  const [failure, setFailure] = useState<{ id: number; message: string } | null>(null)

  const change = (user: AdminUser, disabled: boolean) => {
    setFailure(null)
    update.mutate(
      { id: user.id, disabled },
      {
        onSuccess: () => setConfirmingId(null),
        onError: (error) => {
          setConfirmingId(null)
          setFailure({ id: user.id, message: changeConflictMessage(error) })
        },
      },
    )
  }

  const columns: DataColumn<AdminUser>[] = [
    { key: 'name', header: 'Name', render: (user) => <span className="users-name">{user.name}</span> },
    { key: 'email', header: 'Email', render: (user) => <span className="mono">{user.email}</span> },
    { key: 'source', header: 'Sign-in', render: (user) => sourceWords[user.source] },
    {
      key: 'groups',
      header: 'Groups',
      render: (user) =>
        user.groups.length === 0 ? <span className="users-muted">None</span> : <span className="users-tags">{user.groups.map((group) => <Tag key={group.id}>{group.name}</Tag>)}</span>,
    },
    {
      key: 'last',
      header: 'Last sign-in',
      render: (user) => <span className="users-muted">{formatLastSignIn(user.last_sign_in_at)}</span>,
    },
    {
      key: 'state',
      header: 'State',
      render: (user) =>
        user.disabled ? <StatusPill status="cancelled" label="Disabled" /> : <StatusPill status="succeeded" label="Enabled" />,
    },
    {
      key: 'actions',
      header: 'Actions',
      render: (user) => (
        <div>
          <RowActions
            user={user}
            confirming={confirmingId === user.id}
            busy={update.isPending && update.variables?.id === user.id}
            onEditGroups={() => onEditGroups(user.id)}
            onAskDisable={() => {
              setFailure(null)
              setConfirmingId(user.id)
            }}
            onCancel={() => setConfirmingId(null)}
            onDisable={() => change(user, true)}
            onEnable={() => change(user, false)}
          />
          {failure?.id === user.id && (
            <p className="users-row-error" role="alert">
              {failure.message}
            </p>
          )}
        </div>
      ),
    },
  ]

  return (
    <Card flush>
      <DataTable
        label="Users"
        columns={columns}
        rows={users}
        rowKey={(user) => user.id}
        minWidth={880}
        empty={
          <EmptyState title="No users yet">Add the first user with the form below. Single sign-on users appear after their first sign-in.</EmptyState>
        }
      />
    </Card>
  )
}

interface RowActionsProps {
  user: AdminUser
  confirming: boolean
  busy: boolean
  onEditGroups: () => void
  onAskDisable: () => void
  onCancel: () => void
  onDisable: () => void
  onEnable: () => void
}

function RowActions({ user, confirming, busy, onEditGroups, onAskDisable, onCancel, onDisable, onEnable }: RowActionsProps) {
  const container = useRef<HTMLDivElement>(null)
  const wasConfirming = useRef(false)

  // Move focus into the confirmation when it opens and back to the trigger when it closes.
  useEffect(() => {
    if (confirming) {
      container.current?.querySelector<HTMLElement>('[data-focus="confirm"]')?.focus()
    } else if (wasConfirming.current) {
      container.current?.querySelector<HTMLElement>('[data-focus="disable"]')?.focus()
    }
    wasConfirming.current = confirming
  }, [confirming])

  if (confirming) {
    return (
      <div className="users-confirm" ref={container}>
        <span>Disable {user.name}? Their sessions and tokens stop working straight away.</span>
        <Button data-focus="confirm" size="sm" variant="danger" loading={busy} aria-label={`Confirm disable of ${user.name}`} onClick={onDisable}>
          Disable user
        </Button>
        <Button size="sm" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    )
  }
  return (
    <div className="users-actions" ref={container}>
      <Button size="sm" aria-label={`Edit groups of ${user.name}`} onClick={onEditGroups}>
        Edit groups
      </Button>
      {user.disabled ? (
        <Button size="sm" loading={busy} aria-label={`Enable ${user.name}`} onClick={onEnable}>
          Enable
        </Button>
      ) : (
        <Button data-focus="disable" size="sm" aria-label={`Disable ${user.name}`} onClick={onAskDisable}>
          Disable
        </Button>
      )}
    </div>
  )
}

function GroupEditor({ user, onClose }: { user: AdminUser; onClose: () => void }) {
  const groups = useGroups(true)
  const update = useUpdateUser()
  const [chosen, setChosen] = useState<number[]>(() => user.groups.map((group) => group.id))

  const toggle = (id: number, on: boolean) => setChosen((current) => (on ? [...current, id] : current.filter((value) => value !== id)))

  const save = (event: FormEvent) => {
    event.preventDefault()
    if (!groups.data) {
      return
    }
    // Send ids in the order the groups are listed, whatever order they were ticked in.
    const groupIds = groups.data.groups.filter((group) => chosen.includes(group.id)).map((group) => group.id)
    update.mutate({ id: user.id, group_ids: groupIds }, { onSuccess: onClose })
  }

  return (
    <Card>
      <form className="users-editor" onSubmit={save} aria-label={`Groups of ${user.name}`}>
        <h2>Groups of {user.name}</h2>
        {groups.isPending && <Loading />}
        {groups.isError && <ErrorState error={groups.error} onRetry={() => void groups.refetch()} />}
        {groups.data &&
          (groups.data.groups.length === 0 ? (
            <p className="users-muted">There are no groups yet. Create one under Groups and permissions.</p>
          ) : (
            <fieldset className="users-groups">
              <legend className="ui-visually-hidden">Groups</legend>
              {groups.data.groups.map((group) => (
                <Checkbox key={group.id} label={group.name} checked={chosen.includes(group.id)} onChange={(event) => toggle(group.id, event.target.checked)} />
              ))}
            </fieldset>
          ))}
        {update.isError && <Note tone="error">{changeConflictMessage(update.error)}</Note>}
        <div className="users-buttons">
          <Button type="submit" variant="primary" loading={update.isPending} disabled={!groups.data}>
            Save groups
          </Button>
          <Button onClick={onClose}>Cancel</Button>
        </div>
      </form>
    </Card>
  )
}

interface FormErrors {
  name?: string
  email?: string
  password?: string
  form?: string
}

function CreateUserForm() {
  const create = useCreateUser()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [kind, setKind] = useState<'local' | 'service'>('local')
  const [errors, setErrors] = useState<FormErrors>({})

  const submit = (event: FormEvent) => {
    event.preventDefault()
    // The password is cleared at once, whatever the result.
    const secret = password
    setPassword('')

    const found: FormErrors = {}
    if (name.trim() === '') {
      found.name = 'Enter a name'
    }
    if (!email.includes('@')) {
      found.email = 'Enter a valid email address'
    }
    if (secret.length < MIN_PASSWORD_LENGTH) {
      found.password = `Password must be at least ${MIN_PASSWORD_LENGTH} characters`
    }
    setErrors(found)
    if (Object.keys(found).length > 0) {
      return
    }

    create.mutate(
      { name: name.trim(), email: email.trim(), source: kind, password: secret },
      {
        onSuccess: () => {
          setName('')
          setEmail('')
          setKind('local')
        },
        onError: (error) => {
          setErrors(error.status === 409 ? { email: error.message } : { form: errorMessage(error) })
        },
        // The error is already in local state; reset drops the variables (the password) from the mutation.
        onSettled: () => create.reset(),
      },
    )
  }

  return (
    <Card>
      <form className="users-form" onSubmit={submit} noValidate aria-label="Add a user">
        <h2>Add a user</h2>
        <div className="users-fields">
          <Field label="Name" error={errors.name}>
            <TextInput value={name} onChange={(event) => setName(event.target.value)} autoComplete="off" />
          </Field>
          <Field label="Email" error={errors.email}>
            <TextInput type="email" value={email} onChange={(event) => setEmail(event.target.value)} autoComplete="off" />
          </Field>
          <Field label="Password" hint="At least 12 characters" error={errors.password}>
            <TextInput type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete="new-password" />
          </Field>
          <Field label="Sign-in kind">
            <Select value={kind} onChange={(event) => setKind(event.target.value === 'service' ? 'service' : 'local')}>
              <option value="local">Local</option>
              <option value="service">Service</option>
            </Select>
          </Field>
        </div>
        {errors.form && <Note tone="error">{errors.form}</Note>}
        <div className="users-buttons">
          <Button type="submit" variant="primary" loading={create.isPending}>
            Create user
          </Button>
        </div>
      </form>
    </Card>
  )
}
