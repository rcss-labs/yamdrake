import { useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import type { ApiError } from '../../api/error'
import type { AwsAccount, AwsCredentialInput, AwsCredentialView, AwsMethod } from '../../api/types'
import { Button, Card, DataTable, EmptyState, ErrorState, Field, Loading, Select, TextInput } from '../../ui'
import type { DataColumn } from '../../ui'
import { fieldForMessage, useAwsAccounts, useCreateAwsAccount, useTestAwsAccount } from './connectionsQueries'
import './connections.css'

/** AWS error codes the test can return, in plain words. */
const testFailureWords: Record<string, string> = {
  AccountMismatch: 'The credentials belong to a different AWS account',
  AccessDenied: 'AWS denied access: check the role or key permissions',
  InvalidClientTokenId: 'AWS does not recognise this access key',
  SignatureDoesNotMatch: 'The secret access key does not match the access key ID',
  ExpiredToken: 'The credentials have expired',
}

function describeCredential(view: AwsCredentialView) {
  if (view.method === 'role') {
    return <span className="mono">{view.role_arn}</span>
  }
  return `Access key ending ${view.access_key_id_last4}`
}

function TestCell({ account }: { account: AwsAccount }) {
  const test = useTestAwsAccount()
  const result = test.data
  return (
    <div className="conn-test">
      <Button size="sm" loading={test.isPending} aria-label={`Test ${account.name}`} onClick={() => test.mutate(account.id)}>
        Test
      </Button>
      {result?.ok && (
        <p className="conn-result" data-ok="true">
          {result.message || 'The connection works'}
        </p>
      )}
      {result && !result.ok && (
        <p className="conn-result" data-ok="false" role="alert">
          {testFailureWords[result.error_code] ?? `AWS refused the test with ${result.error_code || 'an error'}`}
        </p>
      )}
      {test.error && (
        <p className="conn-result" data-ok="false" role="alert">
          {errorMessage(test.error)}
        </p>
      )}
    </div>
  )
}

interface CredentialDraft {
  method: AwsMethod
  roleArn: string
  externalId: string
  accessKeyId: string
  secretAccessKey: string
}

const emptyCredential: CredentialDraft = { method: 'role', roleArn: '', externalId: '', accessKeyId: '', secretAccessKey: '' }

function toInput(draft: CredentialDraft): AwsCredentialInput {
  if (draft.method === 'role') {
    return {
      method: 'role',
      role_arn: draft.roleArn.trim(),
      ...(draft.externalId.trim() === '' ? {} : { external_id: draft.externalId.trim() }),
    }
  }
  return { method: 'key', access_key_id: draft.accessKeyId.trim(), secret_access_key: draft.secretAccessKey }
}

interface CredentialBlockProps {
  title: string
  draft: CredentialDraft
  error?: string
  onChange: (next: CredentialDraft) => void
}

function CredentialBlock({ title, draft, error, onChange }: CredentialBlockProps) {
  const set = (patch: Partial<CredentialDraft>) => onChange({ ...draft, ...patch })
  return (
    <fieldset className="conn-block">
      <legend>{title}</legend>
      <Field label={`${title} access method`}>
        <Select value={draft.method} onChange={(event) => set({ method: event.target.value as AwsMethod })}>
          <option value="role">Role (recommended)</option>
          <option value="key">Access key and secret key</option>
        </Select>
      </Field>
      {draft.method === 'role' ? (
        <>
          <Field label={`${title} role ARN`} hint="Yumdrake assumes this role when it needs it; nothing long-lived is stored.">
            <TextInput
              className="mono"
              autoComplete="off"
              placeholder="arn:aws:iam::444444444444:role/yumdrake"
              value={draft.roleArn}
              onChange={(event) => set({ roleArn: event.target.value })}
            />
          </Field>
          <Field label={`${title} external ID`} hint="Optional.">
            <TextInput autoComplete="off" value={draft.externalId} onChange={(event) => set({ externalId: event.target.value })} />
          </Field>
        </>
      ) : (
        <>
          <Field label={`${title} access key ID`}>
            <TextInput className="mono" autoComplete="off" value={draft.accessKeyId} onChange={(event) => set({ accessKeyId: event.target.value })} />
          </Field>
          <Field label={`${title} secret access key`} hint="Stored encrypted. It can be replaced later but never read back.">
            <TextInput
              type="password"
              autoComplete="off"
              value={draft.secretAccessKey}
              onChange={(event) => set({ secretAccessKey: event.target.value })}
            />
          </Field>
        </>
      )}
      {error && (
        <p className="conn-error" role="alert">
          {error}
        </p>
      )}
    </fieldset>
  )
}

const fieldNames = ['name', 'account_id', 'discovery', 'deploy'] as const

function ConnectForm({ onClose }: { onClose: () => void }) {
  const create = useCreateAwsAccount()
  const [name, setName] = useState('')
  const [accountId, setAccountId] = useState('')
  const [discovery, setDiscovery] = useState(emptyCredential)
  const [deploy, setDeploy] = useState(emptyCredential)

  const [error, setError] = useState<ApiError | null>(null)
  const failedField = error ? fieldForMessage(error.message, fieldNames) : undefined
  const messageFor = (field: (typeof fieldNames)[number]) => (error && failedField === field ? error.message : undefined)

  async function submit(event: FormEvent) {
    event.preventDefault()
    const request = { name: name.trim(), account_id: accountId.trim(), discovery: toInput(discovery), deploy: toInput(deploy) }
    // Secrets never stay in the form, whatever the result.
    setDiscovery((current) => ({ ...current, secretAccessKey: '' }))
    setDeploy((current) => ({ ...current, secretAccessKey: '' }))
    setError(null)
    try {
      await create.mutateAsync(request)
      onClose()
    } catch (failure) {
      setError(failure as ApiError)
    } finally {
      // The request body holds the secrets: do not leave it in the mutation cache.
      create.reset()
    }
  }

  return (
    <Card>
      <form className="conn-form" onSubmit={(event) => void submit(event)} noValidate>
        <h2>Connect AWS account</h2>
        <div className="conn-grid">
          <Field label="Name" error={messageFor('name')}>
            <TextInput placeholder="staging" value={name} onChange={(event) => setName(event.target.value)} />
          </Field>
          <Field label="AWS account ID" error={messageFor('account_id')}>
            <TextInput className="mono" placeholder="444444444444" value={accountId} onChange={(event) => setAccountId(event.target.value)} />
          </Field>
        </div>
        <div className="conn-grid">
          <CredentialBlock title="Discovery" draft={discovery} error={messageFor('discovery')} onChange={setDiscovery} />
          <CredentialBlock title="Deployment" draft={deploy} error={messageFor('deploy')} onChange={setDeploy} />
        </div>
        {error && !failedField && (
          <p className="conn-error" role="alert">
            {error.message}
          </p>
        )}
        <div className="conn-actions">
          <Button type="submit" variant="primary" loading={create.isPending}>
            Save account
          </Button>
          <Button onClick={onClose}>Cancel</Button>
        </div>
      </form>
    </Card>
  )
}

export function AWSAccountsSection() {
  const accounts = useAwsAccounts()
  const [formOpen, setFormOpen] = useState(false)

  const columns: DataColumn<AwsAccount>[] = [
    { key: 'name', header: 'Name', render: (account) => <span className="conn-name">{account.name}</span> },
    // Second, so the Test action is visible without scrolling the wide table sideways.
    { key: 'test', header: 'Connection', render: (account) => <TestCell account={account} /> },
    { key: 'id', header: 'Account ID', render: (account) => <span className="mono">{account.account_id}</span> },
    { key: 'discovery', header: 'Discovery connects with', render: (account) => describeCredential(account.discovery) },
    { key: 'deploy', header: 'Deployment connects with', render: (account) => describeCredential(account.deploy) },
    {
      key: 'environments',
      header: 'Used by environments',
      render: (account) => (account.environments.length === 0 ? 'None yet' : account.environments.join(', ')),
    },
  ]

  return (
    <>
      <div className="conn-intro">
        <p>
          Yumdrake can only act in AWS accounts connected here. A role is preferred; an access key also works, encrypted before it is saved
          and never shown again.
        </p>
        {!formOpen && (
          <Button variant="primary" onClick={() => setFormOpen(true)}>
            Connect AWS account
          </Button>
        )}
      </div>
      {formOpen && <ConnectForm onClose={() => setFormOpen(false)} />}
      {accounts.isPending && <Loading />}
      {accounts.error && <ErrorState error={accounts.error} onRetry={() => void accounts.refetch()} />}
      {accounts.data && (
        <Card flush>
          <DataTable
            label="AWS accounts"
            columns={columns}
            rows={accounts.data.accounts}
            rowKey={(account) => account.id}
            minWidth={880}
            empty={
              <EmptyState title="No AWS accounts are connected">
                Connect an account so Yumdrake can discover and deploy services in it.
              </EmptyState>
            }
          />
        </Card>
      )}
    </>
  )
}
