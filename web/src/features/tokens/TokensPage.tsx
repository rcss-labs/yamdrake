import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import type { ApiToken, CreatedToken } from '../../api/types'
import { Button, Card, CodeBlock, DataTable, EmptyState, ErrorState, Field, Loading, Note, PageHeader, Select, TextInput, type DataColumn } from '../../ui'
import { useCreateToken, useRevokeToken, useTokens } from './queries'
import './tokens.css'

const CLI_USAGE = [
  '$ yumdrake login --sso',
  'Opening your browser for single sign-on... signed in as you@example.com',
  '',
  '$ yumdrake jobs list --folder Backend',
  'deploy-backend   Backend and microservices',
  'deploy-billing   Billing service only',
  '',
  '$ yumdrake run deploy-backend --env hetzner-01 \\',
  '    --set microservices=1.4.2 --set microservices.billing=1.4.1',
  'Run #1042 started. Follow it with: yumdrake runs watch 1042',
  '',
  '$ yumdrake run deploy-platform --env hetzner-02 \\',
  '    --only backends,mobile --set backends=2.31.0 --set mobile=3.2.1',
  '',
  '$ yumdrake run deploy-billing --env hetzner-01 --set billing=1.4.2',
  '',
  '$ yumdrake versions backend-socket --env prod-01',
  '2.31.0  2.30.4  2.30.3  2.29.0     (ECR, prod account)',
  '',
  '$ yumdrake approvals approve 1041',
  '',
  '$ yumdrake validate ./yumdrake-config',
  'OK: 5 environments, 33 services, 6 jobs',
].join('\n')

const LIFETIMES = [
  { days: 30, label: '30 days' },
  { days: 90, label: '90 days' },
  { days: 180, label: '180 days' },
  { days: 365, label: '365 days' },
]

function formatDay(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })
}

function formatWhen(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return iso
  }
  return date.toLocaleString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

type CopyState = 'idle' | 'copied' | 'failed'

// The plaintext lives only in this component's state: it is never cached, stored or put in a URL.
function CreateTokenCard() {
  const create = useCreateToken()
  const [name, setName] = useState('')
  const [days, setDays] = useState('90')
  const [nameError, setNameError] = useState<string>()
  const [created, setCreated] = useState<CreatedToken | null>(null)
  const [copy, setCopy] = useState<CopyState>('idle')

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (name.trim() === '') {
      setNameError('Enter a name for the token')
      return
    }
    setNameError(undefined)
    setCreated(null)
    setCopy('idle')
    create.mutate(
      { name: name.trim(), ttl_days: Number(days) },
      {
        onSuccess: (token) => {
          setCreated(token)
          setName('')
        },
      },
    )
  }

  const copyValue = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value)
      setCopy('copied')
    } catch {
      setCopy('failed')
    }
  }

  return (
    <Card className="tokens-create">
      <h2>Create token</h2>
      <form className="tokens-form" onSubmit={submit} noValidate>
        <Field label="Token name" error={nameError}>
          <TextInput value={name} onChange={(event) => setName(event.target.value)} autoComplete="off" />
        </Field>
        <Field label="Expires after">
          <Select value={days} onChange={(event) => setDays(event.target.value)}>
            {LIFETIMES.map((option) => (
              <option key={option.days} value={option.days}>
                {option.label}
              </option>
            ))}
          </Select>
        </Field>
        <Button type="submit" variant="primary" loading={create.isPending}>
          Create token
        </Button>
      </form>
      {create.isError && <Note tone="error">{errorMessage(create.error)}</Note>}
      {created && (
        <div className="tokens-new">
          <p>
            Token <strong>{created.name}</strong> was created.
          </p>
          <p>This is the only time the token is shown</p>
          <div className="tokens-value-row">
            <code className="mono tokens-value" data-testid="new-token-value">
              {created.plaintext}
            </code>
            <Button onClick={() => void copyValue(created.plaintext)}>Copy token</Button>
          </div>
          {copy === 'copied' && <span className="tokens-copy-note">Copied</span>}
          {copy === 'failed' && <span className="tokens-copy-note">Could not copy. Select the value and copy it by hand.</span>}
        </div>
      )}
    </Card>
  )
}

function TokenList() {
  const tokens = useTokens()
  const revoke = useRevokeToken()
  const [confirming, setConfirming] = useState<ApiToken | null>(null)

  if (tokens.isPending) {
    return <Loading />
  }
  if (tokens.isError) {
    return <ErrorState error={tokens.error} onRetry={() => void tokens.refetch()} />
  }

  const columns: DataColumn<ApiToken>[] = [
    { key: 'name', header: 'Name', render: (token) => <span className="mono">{token.name}</span> },
    { key: 'created', header: 'Created', render: (token) => <span className="tokens-nowrap">{formatDay(token.created_at)}</span> },
    {
      key: 'used',
      header: 'Last used',
      render: (token) => <span className="tokens-nowrap">{token.last_used_at ? formatWhen(token.last_used_at) : 'Never'}</span>,
    },
    { key: 'expires', header: 'Expires', render: (token) => <span className="tokens-nowrap">{formatDay(token.expires_at)}</span> },
    {
      key: 'actions',
      header: <span className="ui-visually-hidden">Actions</span>,
      className: 'tokens-actions',
      render: (token) => (
        <Button size="sm" variant="danger" aria-label={`Revoke ${token.name}`} onClick={() => setConfirming(token)}>
          Revoke
        </Button>
      ),
    },
  ]

  return (
    <>
      {confirming && (
        <div className="tokens-revoke" role="group" aria-label={`Revoke ${confirming.name}?`}>
          <p>Anything using this token stops working straight away. This cannot be undone.</p>
          <Button onClick={() => setConfirming(null)}>Cancel</Button>
          <Button
            variant="danger"
            loading={revoke.isPending}
            onClick={() => revoke.mutate(confirming, { onSuccess: () => setConfirming(null) })}
          >
            Revoke token
          </Button>
        </div>
      )}
      {revoke.isError && (
        <div className="tokens-revoke" role="alert">
          <p>{errorMessage(revoke.error)}</p>
        </div>
      )}
      <DataTable
        label="Your CLI tokens"
        columns={columns}
        rows={tokens.data.tokens}
        rowKey={(token) => token.id}
        minWidth={640}
        empty={
          <EmptyState title="You have no CLI tokens">
            Run &quot;yumdrake login&quot; in a terminal, or create a token below for a script.
          </EmptyState>
        }
      />
    </>
  )
}

export function TokensPage() {
  return (
    <>
      <PageHeader
        title="CLI"
        lead="The CLI talks to the same service with the same permissions. Its runs appear in Runs next to the ones started here."
      />
      <CodeBlock label="CLI usage">{CLI_USAGE}</CodeBlock>
      <Card flush>
        <div className="tokens-head">
          <h2>Your CLI tokens</h2>
          <div className="tokens-sub">A token carries your own role and scope, never more</div>
        </div>
        <TokenList />
      </Card>
      <CreateTokenCard />
    </>
  )
}
