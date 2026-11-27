import { useState } from 'react'
import type { FormEvent } from 'react'
import type { CreateGithubConnectionRequest, GithubConnection, GithubMethod } from '../../api/types'
import { Button, Card, DataTable, EmptyState, ErrorState, Field, Loading, Select, TextInput } from '../../ui'
import type { DataColumn } from '../../ui'
import { fieldForMessage, useCreateGithubConnection, useGithubConnections } from './connectionsQueries'
import './connections.css'

const columns: DataColumn<GithubConnection>[] = [
  { key: 'owner', header: 'Organisation or owner', render: (connection) => <span className="conn-name">{connection.owner}</span> },
  { key: 'method', header: 'Access', render: (connection) => (connection.method === 'app' ? 'GitHub App' : 'Access token') },
  {
    key: 'details',
    header: 'Details',
    render: (connection) =>
      connection.method === 'app' ? `App ${connection.app_id}, installation ${connection.installation_id}` : 'Stored encrypted',
  },
]

const tokenFields = ['owner', 'token'] as const
const appFields = ['owner', 'app_id', 'installation_id', 'private_key'] as const

function ConnectForm({ onClose }: { onClose: () => void }) {
  const create = useCreateGithubConnection()
  const [owner, setOwner] = useState('')
  const [method, setMethod] = useState<GithubMethod>('app')
  const [token, setToken] = useState('')
  const [appId, setAppId] = useState('')
  const [installationId, setInstallationId] = useState('')
  const [privateKey, setPrivateKey] = useState('')

  const error = create.error
  const failedField = error ? fieldForMessage<string>(error.message, method === 'app' ? appFields : tokenFields) : undefined
  const messageFor = (field: string) => (error && failedField === field ? error.message : undefined)

  function submit(event: FormEvent) {
    event.preventDefault()
    const request: CreateGithubConnectionRequest =
      method === 'token'
        ? { owner: owner.trim(), method, token }
        : {
            owner: owner.trim(),
            method,
            app_id: Number(appId) || 0,
            installation_id: Number(installationId) || 0,
            private_key: privateKey,
          }
    create.mutate(request, { onSuccess: onClose })
    // Secrets never stay in the form, whatever the result.
    setToken('')
    setPrivateKey('')
  }

  return (
    <Card>
      <form className="conn-form" onSubmit={submit} noValidate>
        <h2>Connect GitHub</h2>
        <div className="conn-grid">
          <Field label="Organisation or owner" error={messageFor('owner')}>
            <TextInput placeholder="example-org" autoComplete="off" value={owner} onChange={(event) => setOwner(event.target.value)} />
          </Field>
          <Field label="Access method">
            <Select value={method} onChange={(event) => setMethod(event.target.value as GithubMethod)}>
              <option value="app">GitHub App (recommended)</option>
              <option value="token">Access token</option>
            </Select>
          </Field>
        </div>
        {method === 'token' ? (
          <Field label="Access token" error={messageFor('token')} hint="Stored encrypted and never shown again.">
            <TextInput type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} />
          </Field>
        ) : (
          <>
            <div className="conn-grid">
              <Field label="App ID" error={messageFor('app_id')}>
                <TextInput inputMode="numeric" autoComplete="off" value={appId} onChange={(event) => setAppId(event.target.value)} />
              </Field>
              <Field label="Installation ID" error={messageFor('installation_id')}>
                <TextInput inputMode="numeric" autoComplete="off" value={installationId} onChange={(event) => setInstallationId(event.target.value)} />
              </Field>
            </div>
            <Field label="Private key" error={messageFor('private_key')} hint="Paste the PEM file. It is stored encrypted and never shown again.">
              <textarea
                className="conn-textarea"
                autoComplete="off"
                spellCheck={false}
                value={privateKey}
                onChange={(event) => setPrivateKey(event.target.value)}
              />
            </Field>
          </>
        )}
        <p className="conn-hint">Read-only access to the chosen repositories is enough.</p>
        {error && !failedField && (
          <p className="conn-error" role="alert">
            {error.message}
          </p>
        )}
        <div className="conn-actions">
          <Button type="submit" variant="primary" loading={create.isPending}>
            Save connection
          </Button>
          <Button onClick={onClose}>Cancel</Button>
        </div>
      </form>
    </Card>
  )
}

export function GitHubSection() {
  const connections = useGithubConnections()
  const [formOpen, setFormOpen] = useState(false)

  return (
    <>
      <div className="conn-intro">
        <p>
          Yumdrake reads every YAML file, jobs and inventory, from GitHub, and the copy-config step reads env files from GitHub. Both use
          the connections here.
        </p>
        {!formOpen && (
          <Button variant="primary" onClick={() => setFormOpen(true)}>
            Connect GitHub
          </Button>
        )}
      </div>
      {formOpen && <ConnectForm onClose={() => setFormOpen(false)} />}
      {connections.isPending && <Loading />}
      {connections.error && <ErrorState error={connections.error} onRetry={() => void connections.refetch()} />}
      {connections.data && (
        <Card flush>
          <DataTable
            label="GitHub connections"
            columns={columns}
            rows={connections.data.connections}
            rowKey={(connection) => connection.id}
            minWidth={560}
            empty={
              <EmptyState title="No GitHub connections">
                Connect an organisation or owner so Yumdrake can read your repositories.
              </EmptyState>
            }
          />
        </Card>
      )}
    </>
  )
}
