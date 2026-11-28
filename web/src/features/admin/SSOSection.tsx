import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import type { SsoSettings, SsoTestResult } from '../../api/types'
import { Button, Card, ErrorState, Field, Loading, Note, StatusPill, TextInput } from '../../ui'
import { useSaveSso, useSsoSettings, useTestSso } from './ssoQueries'
import './sso.css'

export function SSOSection() {
  const settings = useSsoSettings()
  if (settings.isPending) {
    return <Loading />
  }
  if (settings.error || !settings.data) {
    return <ErrorState error={settings.error ?? new Error('The settings could not be loaded.')} onRetry={() => void settings.refetch()} />
  }
  return <SsoForm settings={settings.data} />
}

type CopyState = 'idle' | 'copied' | 'failed'

function SsoForm({ settings }: { settings: SsoSettings }) {
  const save = useSaveSso()
  const test = useTestSso()
  const [enabled, setEnabled] = useState(settings.enabled)
  const [metadata, setMetadata] = useState(settings.idp_metadata_xml)
  const [entityId, setEntityId] = useState(settings.entity_id)
  const [groupsAttribute, setGroupsAttribute] = useState(settings.groups_attribute)
  const [emailAttribute, setEmailAttribute] = useState(settings.email_attribute)
  const [nameAttribute, setNameAttribute] = useState(settings.name_attribute)
  const [copy, setCopy] = useState<CopyState>('idle')
  const [result, setResult] = useState<SsoTestResult | null>(null)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    save.mutate({
      enabled,
      idp_metadata_xml: metadata,
      // Left as the default, it is saved empty so that it keeps following the public URL.
      entity_id: entityId === settings.entity_id_default ? '' : entityId,
      groups_attribute: groupsAttribute,
      email_attribute: emailAttribute,
      name_attribute: nameAttribute,
    })
  }

  const runTest = () => {
    setResult(null)
    test.mutate(undefined, { onSuccess: setResult })
  }

  const copyUrl = async () => {
    try {
      await navigator.clipboard.writeText(settings.metadata_url)
      setCopy('copied')
    } catch {
      setCopy('failed')
    }
  }

  return (
    <Card>
      <form className="sso-form" onSubmit={submit} aria-label="Single sign-on settings">
        <h2>SAML single sign-on</h2>
        <label className="sso-switch">
          <input
            type="checkbox"
            role="switch"
            aria-label="Single sign-on enabled"
            checked={enabled}
            onChange={(event) => setEnabled(event.target.checked)}
          />
          <span>{enabled ? 'Single sign-on is on' : 'Single sign-on is off'}</span>
        </label>

        <Field label="Identity provider metadata (XML)" hint="Paste the metadata document from your identity provider.">
          <textarea className="ui-input sso-metadata mono" rows={8} value={metadata} onChange={(event) => setMetadata(event.target.value)} />
        </Field>

        <div className="sso-grid">
          <Field label="Service provider entity ID">
            <TextInput value={entityId} onChange={(event) => setEntityId(event.target.value)} />
          </Field>
          <Field label="Groups attribute" hint="The attribute carrying the person's groups.">
            <TextInput value={groupsAttribute} onChange={(event) => setGroupsAttribute(event.target.value)} />
          </Field>
          <Field label="Email attribute" hint="Leave empty to use the name ID.">
            <TextInput value={emailAttribute} onChange={(event) => setEmailAttribute(event.target.value)} />
          </Field>
          <Field label="Name attribute">
            <TextInput value={nameAttribute} onChange={(event) => setNameAttribute(event.target.value)} />
          </Field>
        </div>

        <div className="sso-facts">
          <div>
            <div className="sso-key">Service provider metadata URL</div>
            <div className="sso-copy-row">
              <span className="mono sso-url">{settings.metadata_url}</span>
              <Button size="sm" onClick={() => void copyUrl()}>
                Copy metadata URL
              </Button>
              {copy === 'copied' && <span className="sso-feedback">Copied</span>}
              {copy === 'failed' && <span className="sso-feedback">Could not copy. Select the address and copy it by hand.</span>}
            </div>
          </div>
          <div>
            <div className="sso-key">Sign-in callback (ACS) URL</div>
            <span className="mono sso-url">{settings.acs_url}</span>
          </div>
        </div>

        <div className="sso-actions">
          <Button type="submit" variant="primary" loading={save.isPending}>
            Save
          </Button>
          {save.isSuccess && !save.isPending && <span className="sso-feedback">Settings saved</span>}
          {save.error && <Note tone="error">{errorMessage(save.error)}</Note>}
        </div>

        <div className="sso-actions">
          <Button onClick={runTest} loading={test.isPending}>
            Test
          </Button>
          <span className="sso-feedback">Tests the saved metadata.</span>
          {result && (
            <span role="status" className="sso-result" data-ok={result.ok}>
              <StatusPill status={result.ok ? 'succeeded' : 'failed'} label={result.ok ? 'Test passed' : 'Test failed'} /> {result.message}
            </span>
          )}
          {test.error && <Note tone="error">{errorMessage(test.error)}</Note>}
        </div>
      </form>
    </Card>
  )
}
