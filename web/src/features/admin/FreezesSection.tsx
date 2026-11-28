import { useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import type { Freeze } from '../../api/types'
import { Button, Card, ConfirmDialog, DataTable, EmptyState, ErrorState, Field, Loading, Note, Select, TextInput, Time } from '../../ui'
import type { DataColumn } from '../../ui'
import { useCreateFreeze, useLiftFreeze } from '../freezes/freezeActions'
import { useFreezes } from '../runs/queries'
import { useInventory } from './connectionsQueries'
import './connections.css'

const hourMs = 3_600_000
const dayMs = 24 * hourMs
const maxDays = 30

const presets: Record<string, { label: string; ms: number }> = {
  '1h': { label: '1 hour', ms: hourMs },
  '4h': { label: '4 hours', ms: 4 * hourMs },
  '1d': { label: '1 day', ms: dayMs },
}

const scopeOf = (freeze: Freeze) => freeze.environment || freeze.label

function FreezeForm() {
  const inventory = useInventory()
  const create = useCreateFreeze()
  const [kind, setKind] = useState<'label' | 'environment'>('label')
  const [label, setLabel] = useState('')
  const [environment, setEnvironment] = useState('')
  const [reason, setReason] = useState('')
  const [length, setLength] = useState('4h')
  const [days, setDays] = useState('')
  const [errors, setErrors] = useState<{ scope?: string; reason?: string; days?: string; form?: string }>({})

  const environments = inventory.data?.environments ?? []
  const labels = [...new Set(environments.map((candidate) => candidate.label))].sort()

  async function submit(event: FormEvent) {
    event.preventDefault()
    const found: typeof errors = {}
    const scope = kind === 'label' ? label : environment
    if (scope === '') found.scope = kind === 'label' ? 'Choose a label' : 'Choose an environment'
    if (reason.trim() === '') found.reason = 'Give a reason so others know why'
    let duration = presets[length]?.ms ?? 0
    if (length === 'custom') {
      const count = Number(days)
      if (!Number.isInteger(count) || count < 1 || count > maxDays) {
        found.days = `A freeze lasts between 1 and ${maxDays} days`
      } else {
        duration = count * dayMs
      }
    }
    setErrors(found)
    if (Object.keys(found).length > 0) return
    try {
      await create.mutateAsync({
        ...(kind === 'label' ? { label } : { environment }),
        reason: reason.trim(),
        expires_at: new Date(Date.now() + duration).toISOString(),
      })
      setReason('')
      setDays('')
    } catch (failure) {
      setErrors({ form: errorMessage(failure) })
    }
  }

  return (
    <Card>
      <form className="conn-form" onSubmit={(event) => void submit(event)} noValidate>
        <h2>Freeze deployments</h2>
        <div className="conn-grid">
          <Field label="Freeze">
            <Select value={kind} onChange={(event) => setKind(event.target.value as 'label' | 'environment')}>
              <option value="label">A label</option>
              <option value="environment">An environment</option>
            </Select>
          </Field>
          {kind === 'label' ? (
            <Field label="Label" error={errors.scope}>
              <Select value={label} onChange={(event) => setLabel(event.target.value)}>
                <option value="">Choose a label</option>
                {labels.map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </Select>
            </Field>
          ) : (
            <Field label="Environment" error={errors.scope}>
              <Select value={environment} onChange={(event) => setEnvironment(event.target.value)}>
                <option value="">Choose an environment</option>
                {environments.map((candidate) => (
                  <option key={candidate.name} value={candidate.name}>
                    {candidate.name}
                  </option>
                ))}
              </Select>
            </Field>
          )}
        </div>
        {/* The kit has no Textarea; a single-line TextInput is enough for a one-sentence reason. */}
        <Field label="Reason" error={errors.reason} hint="Shown to everyone who tries to deploy while the freeze lasts.">
          <TextInput autoComplete="off" value={reason} onChange={(event) => setReason(event.target.value)} />
        </Field>
        <div className="conn-grid">
          <Field label="Length">
            <Select value={length} onChange={(event) => setLength(event.target.value)}>
              {Object.entries(presets).map(([value, preset]) => (
                <option key={value} value={value}>
                  {preset.label}
                </option>
              ))}
              <option value="custom">Custom</option>
            </Select>
          </Field>
          {length === 'custom' && (
            <Field label="Days" error={errors.days} hint={`Up to ${maxDays} days.`}>
              <TextInput inputMode="numeric" autoComplete="off" value={days} onChange={(event) => setDays(event.target.value)} />
            </Field>
          )}
        </div>
        {errors.form && <Note tone="error">{errors.form}</Note>}
        <div className="conn-actions">
          <Button type="submit" variant="primary" loading={create.isPending}>
            Freeze deployments
          </Button>
        </div>
      </form>
    </Card>
  )
}

export function FreezesSection() {
  const freezes = useFreezes()
  const lift = useLiftFreeze()
  const [lifting, setLifting] = useState<Freeze | null>(null)
  const [failure, setFailure] = useState('')

  async function confirmLift() {
    const freeze = lifting
    if (!freeze) return
    setLifting(null)
    setFailure('')
    try {
      await lift.mutateAsync({ id: freeze.id })
    } catch (error) {
      setFailure(errorMessage(error))
    }
  }

  const activeColumns: DataColumn<Freeze>[] = [
    { key: 'scope', header: 'Scope', render: (freeze) => <span className="conn-name">{scopeOf(freeze)}</span> },
    { key: 'reason', header: 'Reason', render: (freeze) => freeze.reason },
    { key: 'who', header: 'Frozen by', render: (freeze) => freeze.created_by_name },
    { key: 'until', header: 'Until', render: (freeze) => <Time iso={freeze.expires_at} /> },
    {
      key: 'lift',
      header: 'Lift',
      render: (freeze) => (
        <Button size="sm" aria-label={`Lift ${scopeOf(freeze)}`} onClick={() => setLifting(freeze)}>
          Lift
        </Button>
      ),
    },
  ]
  const endedColumns: DataColumn<Freeze>[] = [
    { key: 'scope', header: 'Scope', render: (freeze) => <span className="conn-name">{scopeOf(freeze)}</span> },
    { key: 'reason', header: 'Reason', render: (freeze) => freeze.reason },
    { key: 'who', header: 'Frozen by', render: (freeze) => freeze.created_by_name },
    {
      key: 'ended',
      header: 'Ended',
      render: (freeze) => (
        <>
          <Time iso={freeze.ended_at ?? freeze.expires_at} />
          {freeze.ended_by_name ? `, lifted by ${freeze.ended_by_name}` : ', expired'}
        </>
      ),
    },
  ]

  return (
    <>
      <div className="conn-intro">
        <p>While a freeze lasts, nobody can start a deployment in its label or environment. Runs already going are not stopped.</p>
      </div>
      {failure && <Note tone="error">{failure}</Note>}
      {freezes.isPending && <Loading />}
      {freezes.error && <ErrorState error={freezes.error} onRetry={() => void freezes.refetch()} />}
      {freezes.data && (
        <>
          <Card flush>
            <DataTable
              label="Active freezes"
              columns={activeColumns}
              rows={freezes.data.active}
              rowKey={(freeze) => freeze.id}
              minWidth={640}
              empty={<EmptyState title="No active freezes">Deployments are open everywhere.</EmptyState>}
            />
          </Card>
          <FreezeForm />
          <h2 className="conn-title">Recently ended</h2>
          <Card flush>
            <DataTable
              label="Ended freezes"
              columns={endedColumns}
              rows={freezes.data.ended}
              rowKey={(freeze) => freeze.id}
              minWidth={640}
              empty={<EmptyState title="No ended freezes">Freezes appear here after they expire or are lifted.</EmptyState>}
            />
          </Card>
        </>
      )}
      <ConfirmDialog
        open={lifting !== null}
        title={`Lift the freeze on ${lifting ? scopeOf(lifting) : ''}?`}
        body="Deployments there can start again at once. Nothing already stopped is resumed."
        confirmLabel="Lift freeze"
        onConfirm={() => void confirmLift()}
        onCancel={() => setLifting(null)}
      />
    </>
  )
}
