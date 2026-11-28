import { useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage } from '../../api/client'
import { Button, Card, ErrorState, Field, Loading, Note, TextInput, Time } from '../../ui'
import { useSlack } from '../runs/queries'
import { testFailureMessage, useSaveSlackToken, useTestSlack } from './slackQueries'
import './connections.css'

function TokenForm() {
  const save = useSaveSlackToken()
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (token.trim() === '') {
      setError('Paste the bot token first')
      return
    }
    const value = token.trim()
    // The secret never stays in the form, whatever the result.
    setToken('')
    setError('')
    setSaved(false)
    try {
      await save.mutateAsync({ token: value })
      setSaved(true)
    } catch (failure) {
      setError(errorMessage(failure))
    } finally {
      // The request body holds the token: do not leave it in the mutation cache.
      save.reset()
    }
  }

  return (
    <form className="conn-form" onSubmit={(event) => void submit(event)} noValidate>
      <h2>Set a new token</h2>
      <Field label="Bot token" error={error || undefined} hint="Stored encrypted and never shown again. Saving replaces the old token.">
        <TextInput type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} />
      </Field>
      {saved && <p className="conn-saved" role="status">Token saved</p>}
      <div className="conn-actions">
        <Button type="submit" variant="primary" loading={save.isPending}>
          Save token
        </Button>
      </div>
    </form>
  )
}

function TestForm() {
  const test = useTestSlack()
  const [channel, setChannel] = useState('')
  const [error, setError] = useState('')

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (channel.trim() === '') {
      setError('Give a channel, for example #deployments')
      return
    }
    setError('')
    test.reset()
    try {
      await test.mutateAsync({ channel: channel.trim() })
    } catch {
      // Shown from test.error below.
    }
  }

  const result = test.data
  return (
    <form className="conn-form" onSubmit={(event) => void submit(event)} noValidate>
      <h2>Send a test message</h2>
      <Field label="Channel" error={error || undefined} hint="The bot must be a member of the channel.">
        <TextInput placeholder="#deployments" autoComplete="off" value={channel} onChange={(event) => setChannel(event.target.value)} />
      </Field>
      <div className="conn-actions">
        <Button type="submit" loading={test.isPending}>
          Send test message
        </Button>
      </div>
      {result && (
        <p className="conn-result" data-ok={result.ok} role="status">
          {result.ok ? 'Slack accepted the test message' : testFailureMessage(result.code)}
        </p>
      )}
      {test.error && <Note tone="error">{errorMessage(test.error)}</Note>}
    </form>
  )
}

export function SlackSection() {
  const slack = useSlack()
  return (
    <>
      <div className="conn-intro">
        <p>Yumdrake posts run results and approval requests to Slack with a bot token. The token is a secret and is stored encrypted.</p>
      </div>
      {slack.isPending && <Loading />}
      {slack.error && <ErrorState error={slack.error} onRetry={() => void slack.refetch()} />}
      {slack.data && (
        <Card>
          <div className="conn-saved">
            {slack.data.token_set ? (
              <p>
                A token is set, saved <Time iso={slack.data.set_at} />
                {slack.data.set_by_name ? ` by ${slack.data.set_by_name}` : ''}.
              </p>
            ) : (
              <p>No token is set</p>
            )}
          </div>
          <TokenForm />
          <TestForm />
        </Card>
      )}
    </>
  )
}
