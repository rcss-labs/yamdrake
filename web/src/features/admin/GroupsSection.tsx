import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import { ApiError } from '../../api/error'
import type { Action, AdminGrant, AdminGroup, CreateGrantRequest, IdpMapping } from '../../api/types'
import { Button, Card, DataTable, EmptyState, ErrorState, Field, Loading, Note, Select, TextInput } from '../../ui'
import {
  useAddGrant,
  useCreateGroup,
  useFolderChoices,
  useGrants,
  useGroups,
  useInventoryChoices,
  useMappings,
  usePutMapping,
  useRemoveGrant,
  useRemoveMapping,
  useSetGroupAdmin,
} from './groupsQueries'
import { changeConflictMessage } from './messages'
import './groups.css'

// The permission rule (Plan 3): these four actions apply everywhere and take no scope.
const GLOBAL_ACTIONS: readonly Action[] = ['manage_jobs', 'manage_connections', 'manage_access', 'view_audit']
// "view" is always allowed to everyone, so it is never granted.
const GRANTABLE_ACTIONS: readonly Action[] = [
  'view_logs',
  'run',
  'cancel',
  'approve',
  'restore',
  ...GLOBAL_ACTIONS,
]

const ACTION_WORDS: Record<Action, string> = {
  view: 'View',
  view_logs: 'View logs',
  run: 'Run jobs',
  cancel: 'Cancel runs',
  approve: 'Approve runs',
  restore: 'Restore versions',
  manage_jobs: 'Manage jobs',
  manage_connections: 'Manage connections',
  manage_access: 'Manage access',
  view_audit: 'View audit log',
}

const isGlobal = (action: Action) => GLOBAL_ACTIONS.includes(action)

function describeError(error: unknown): string {
  if (error instanceof ApiError && error.status === 403) {
    return `${error.message || 'You do not have permission to do this.'}`
  }
  return errorMessage(error)
}

function memberCount(count: number): string {
  return `${count} ${count === 1 ? 'member' : 'members'}`
}

export function GroupsSection() {
  const groups = useGroups()
  const grants = useGrants()
  const mappings = useMappings()
  const folders = useFolderChoices()
  const inventory = useInventoryChoices()

  if (groups.isPending || grants.isPending || mappings.isPending) {
    return <Loading />
  }
  const failed = groups.error ?? grants.error ?? mappings.error
  if (failed || !groups.data || !grants.data || !mappings.data) {
    return (
      <ErrorState
        error={failed ?? new Error('The groups could not be loaded.')}
        onRetry={() => {
          void groups.refetch()
          void grants.refetch()
          void mappings.refetch()
        }}
      />
    )
  }

  const folderList = folders.data?.folders ?? []
  const environments = inventory.data?.environments ?? []
  const labels = [...new Set(environments.map((environment) => environment.label))].sort()

  return (
    <div className="groups-stack">
      <CreateGroupForm />
      {groups.data.groups.length === 0 && (
        <Card>
          <EmptyState title="No groups yet">Create a group above, then add grants to it.</EmptyState>
        </Card>
      )}
      {groups.data.groups.map((group) => (
        <GroupCard
          key={group.id}
          group={group}
          grants={grants.data.grants.filter((grant) => grant.group_id === group.id)}
          folders={folderList}
          environmentNames={environments.map((environment) => environment.name)}
          labels={labels}
        />
      ))}
      <MappingsCard mappings={mappings.data.mappings} groups={groups.data.groups} />
    </div>
  )
}

function CreateGroupForm() {
  const create = useCreateGroup()
  const [name, setName] = useState('')

  const submit = (event: FormEvent) => {
    event.preventDefault()
    create.mutate({ name: name.trim() }, { onSuccess: () => setName('') })
  }

  return (
    <Card>
      <form className="groups-inline-form" onSubmit={submit} aria-label="Create group">
        <Field label="Group name" error={create.error ? describeError(create.error) : undefined}>
          <TextInput value={name} onChange={(event) => setName(event.target.value)} />
        </Field>
        <Button type="submit" variant="primary" loading={create.isPending} disabled={name.trim() === ''}>
          Create group
        </Button>
      </form>
    </Card>
  )
}

interface GroupCardProps {
  group: AdminGroup
  grants: AdminGrant[]
  folders: { id: number; name: string }[]
  environmentNames: string[]
  labels: string[]
}

function GroupCard({ group, grants, folders, environmentNames, labels }: GroupCardProps) {
  const setAdmin = useSetGroupAdmin()
  const removeGrant = useRemoveGrant()
  const [confirming, setConfirming] = useState<number | null>(null)
  const headingId = `group-${group.id}-name`

  const adminError = setAdmin.error
    ? setAdmin.error.status === 409
      ? changeConflictMessage(setAdmin.error)
      : describeError(setAdmin.error)
    : undefined

  const folderName = (id: number) => folders.find((folder) => folder.id === id)?.name ?? `Folder ${id}`

  return (
    <section className="ui-card groups-card" aria-labelledby={headingId}>
      <header className="groups-card-head">
        <h2 id={headingId}>{group.name}</h2>
        <span className="groups-meta">{memberCount(group.member_count)}</span>
        <label className="groups-switch">
          <input
            type="checkbox"
            role="switch"
            aria-label={`Administrator group: ${group.name}`}
            checked={group.admin}
            disabled={setAdmin.isPending}
            onChange={(event) => setAdmin.mutate({ id: group.id, admin: event.target.checked })}
          />
          <span>Administrator group</span>
        </label>
      </header>
      {adminError && <Note tone="error">{adminError}</Note>}

      <DataTable
        label={`Grants of ${group.name}`}
        rows={grants}
        rowKey={(grant) => grant.id}
        minWidth={520}
        empty={<EmptyState title="No grants yet">Add one below. A group with no grants can only view.</EmptyState>}
        columns={[
          { key: 'action', header: 'Action', render: (grant) => ACTION_WORDS[grant.action] },
          {
            key: 'where',
            header: 'Label or environment',
            render: (grant) => {
              if (isGlobal(grant.action)) return 'Not scoped'
              const parts = [
                grant.label ? `Label ${grant.label}` : '',
                grant.environment ? `Environment ${grant.environment}` : '',
              ].filter(Boolean)
              return parts.length > 0 ? parts.join(', ') : 'Any environment'
            },
          },
          {
            key: 'folder',
            header: 'Folder',
            render: (grant) => (isGlobal(grant.action) ? 'Not scoped' : grant.folder_id ? folderName(grant.folder_id) : 'Any folder'),
          },
          {
            key: 'remove',
            header: <span className="ui-visually-hidden">Remove</span>,
            className: 'groups-actions',
            render: (grant) =>
              confirming === grant.id ? (
                <span className="groups-confirm">
                  <Button
                    size="sm"
                    variant="danger"
                    loading={removeGrant.isPending}
                    onClick={() => removeGrant.mutate(grant.id, { onSettled: () => setConfirming(null) })}
                  >
                    Confirm remove
                  </Button>
                  <Button size="sm" onClick={() => setConfirming(null)}>
                    Keep grant
                  </Button>
                </span>
              ) : (
                <Button size="sm" onClick={() => setConfirming(grant.id)}>
                  Remove grant
                </Button>
              ),
          },
        ]}
      />
      {removeGrant.error && <Note tone="error">{describeError(removeGrant.error)}</Note>}

      <AddGrantForm group={group} folders={folders} environmentNames={environmentNames} labels={labels} />
    </section>
  )
}

interface AddGrantFormProps {
  group: AdminGroup
  folders: { id: number; name: string }[]
  environmentNames: string[]
  labels: string[]
}

function AddGrantForm({ group, folders, environmentNames, labels }: AddGrantFormProps) {
  const add = useAddGrant()
  const [action, setAction] = useState<Action>('run')
  const [label, setLabel] = useState('')
  const [environment, setEnvironment] = useState('')
  const [folderId, setFolderId] = useState('')
  const scoped = !isGlobal(action)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const body: CreateGrantRequest = { group_id: group.id, action }
    if (scoped) {
      if (label) body.label = label
      if (environment) body.environment = environment
      if (folderId) body.folder_id = Number(folderId)
    }
    add.mutate(body, {
      onSuccess: () => {
        setLabel('')
        setEnvironment('')
        setFolderId('')
      },
    })
  }

  return (
    <form className="groups-grant-form" onSubmit={submit} aria-label={`Add grant to ${group.name}`}>
      <Field label="Action">
        <Select value={action} onChange={(event) => setAction(event.target.value as Action)}>
          {GRANTABLE_ACTIONS.map((value) => (
            <option key={value} value={value}>
              {ACTION_WORDS[value]}
            </option>
          ))}
        </Select>
      </Field>
      {scoped && (
        <>
          <Field label="Label" hint={environment || label ? 'A grant has a label or an environment, not both.' : undefined}>
            <Select
              value={label}
              disabled={environment !== ''}
              onChange={(event) => setLabel(event.target.value)}
            >
              <option value="">Any label</option>
              {labels.map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Environment">
            <Select
              value={environment}
              disabled={label !== ''}
              onChange={(event) => setEnvironment(event.target.value)}
            >
              <option value="">Any environment</option>
              {environmentNames.map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Folder">
            <Select value={folderId} onChange={(event) => setFolderId(event.target.value)}>
              <option value="">Any folder</option>
              {folders.map((folder) => (
                <option key={folder.id} value={folder.id}>
                  {folder.name}
                </option>
              ))}
            </Select>
          </Field>
        </>
      )}
      <Button type="submit" variant="primary" loading={add.isPending}>
        Add grant
      </Button>
      {!scoped && <p className="groups-hint">This action applies everywhere and has no scope.</p>}
      {add.error && <Note tone="error">{describeError(add.error)}</Note>}
    </form>
  )
}

function MappingsCard({ mappings, groups }: { mappings: IdpMapping[]; groups: AdminGroup[] }) {
  const put = usePutMapping()
  const remove = useRemoveMapping()
  const [confirming, setConfirming] = useState<string | null>(null)
  const [idpGroup, setIdpGroup] = useState('')
  const [groupId, setGroupId] = useState('')
  const chosenGroup = groupId || String(groups[0]?.id ?? '')

  const submit = (event: FormEvent) => {
    event.preventDefault()
    put.mutate({ idpGroup: idpGroup.trim(), group_id: Number(chosenGroup) }, { onSuccess: () => setIdpGroup('') })
  }

  return (
    <Card flush className="groups-mappings">
      <div className="groups-mappings-head">
        <h2>Identity provider group mappings</h2>
        <p className="groups-hint">People who sign in with single sign-on join the Yumdrake group mapped to each group the identity provider reports.</p>
      </div>
      <DataTable
        label="Identity provider group mappings"
        rows={mappings}
        rowKey={(mapping) => mapping.idp_group}
        minWidth={480}
        empty={<EmptyState title="No identity provider groups are mapped">Add a mapping below so people land in the right group when they sign in.</EmptyState>}
        columns={[
          { key: 'idp', header: 'Identity provider group', className: 'mono', render: (mapping) => mapping.idp_group },
          {
            key: 'group',
            header: 'Yumdrake group',
            render: (mapping) => (
              <Select
                size="sm"
                aria-label={`Yumdrake group for ${mapping.idp_group}`}
                value={String(mapping.group_id)}
                onChange={(event) => put.mutate({ idpGroup: mapping.idp_group, group_id: Number(event.target.value) })}
              >
                {groups.map((group) => (
                  <option key={group.id} value={group.id}>
                    {group.name}
                  </option>
                ))}
              </Select>
            ),
          },
          {
            key: 'remove',
            header: <span className="ui-visually-hidden">Remove</span>,
            className: 'groups-actions',
            render: (mapping) =>
              confirming === mapping.idp_group ? (
                <span className="groups-confirm">
                  <Button
                    size="sm"
                    variant="danger"
                    loading={remove.isPending}
                    onClick={() => remove.mutate(mapping.idp_group, { onSettled: () => setConfirming(null) })}
                  >
                    Confirm remove
                  </Button>
                  <Button size="sm" onClick={() => setConfirming(null)}>
                    Keep mapping
                  </Button>
                </span>
              ) : (
                <Button size="sm" onClick={() => setConfirming(mapping.idp_group)}>
                  Remove mapping
                </Button>
              ),
          },
        ]}
      />
      <form className="groups-mapping-form" onSubmit={submit} aria-label="Add mapping">
        <Field label="Identity provider group">
          <TextInput value={idpGroup} onChange={(event) => setIdpGroup(event.target.value)} />
        </Field>
        <Field label="Yumdrake group">
          <Select value={chosenGroup} onChange={(event) => setGroupId(event.target.value)}>
            {groups.map((group) => (
              <option key={group.id} value={group.id}>
                {group.name}
              </option>
            ))}
          </Select>
        </Field>
        <Button type="submit" variant="primary" loading={put.isPending} disabled={idpGroup.trim() === '' || groups.length === 0}>
          Add mapping
        </Button>
      </form>
      {(put.error || remove.error) && <Note tone="error">{describeError(put.error ?? remove.error)}</Note>}
    </Card>
  )
}
