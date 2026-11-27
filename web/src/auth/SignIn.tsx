import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ApiError, api } from '../api/client'
import type { AuthMethods, Me } from '../api/types'
import { safeNext } from './next'
import { startSession } from './sessionCache'
import './sign-in.css'

export function waitMessage(retryAfterSeconds: number | undefined): string {
  if (retryAfterSeconds === undefined) {
    return 'Too many attempts. Try again later'
  }
  const minutes = Math.max(1, Math.ceil(retryAfterSeconds / 60))
  return `Too many attempts. Try again in ${minutes} ${minutes === 1 ? 'minute' : 'minutes'}`
}

function signInMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) {
      return 'Email or password is not correct'
    }
    if (error.status === 429) {
      return waitMessage(error.retryAfterSeconds)
    }
    return error.message
  }
  return 'Something went wrong. Try again.'
}

/** The SSO link is offered only when the server says SAML is enabled; a 404 or any failure means it is not. */
function useSsoEnabled(): boolean {
  const methods = useQuery({
    queryKey: ['auth', 'methods'],
    queryFn: () => api<AuthMethods>('GET', '/api/v1/auth/methods'),
    retry: false,
    staleTime: Infinity,
  })
  return methods.data?.saml === true
}

/** The sign-in page. Where to go afterwards is decided by SessionProvider from the `next` address. */
export function SignIn() {
  const queryClient = useQueryClient()
  const ssoEnabled = useSsoEnabled()
  const [params] = useSearchParams()
  // The server returns the person to this same-site path after the identity provider answers.
  const ssoStartHref = `/api/v1/auth/saml/start?next=${encodeURIComponent(safeNext(params.get('next')))}`
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [message, setMessage] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const passwordInput = useRef<HTMLInputElement>(null)

  async function submit(event: FormEvent) {
    event.preventDefault()
    const attempt = { email: email.trim(), password }
    // The password is not kept in state past this point, whatever the outcome.
    setPassword('')
    setMessage('')
    setSubmitting(true)
    try {
      const me = await api<Me>('POST', '/api/v1/auth/login', attempt)
      startSession(queryClient, me)
    } catch (error) {
      setMessage(signInMessage(error))
      setSubmitting(false)
      passwordInput.current?.focus()
    }
  }

  return (
    <main className="sign-in">
      <div className="sign-in-card">
        <div>
          <h1 className="sign-in-brand">
            <span className="sign-in-logo" aria-hidden="true">
              <span style={{ height: 10 }} />
              <span style={{ height: 16 }} />
              <span style={{ height: 6 }} />
            </span>
            Yumdrake
          </h1>
          <p className="sign-in-sub">Deployment jobs for DevOps, QA and developers</p>
        </div>
        {ssoEnabled && (
          <a className="sign-in-button sign-in-primary" href={ssoStartHref}>
            Sign in with company SSO
          </a>
        )}
        <form className={`sign-in-form${ssoEnabled ? ' sign-in-separated' : ''}`} onSubmit={(event) => void submit(event)}>
          <div>
            <h2>{ssoEnabled ? 'Local account' : 'Sign in'}</h2>
            {ssoEnabled && <p className="sign-in-sub">Only for the break-glass admin and service accounts</p>}
          </div>
          <label className="sign-in-field">
            Email
            <input type="email" name="email" autoComplete="username" required value={email} onChange={(event) => setEmail(event.target.value)} />
          </label>
          <label className="sign-in-field">
            Password
            <input
              ref={passwordInput}
              type="password"
              name="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </label>
          {message !== '' && (
            <p className="sign-in-error" role="alert">
              {message}
            </p>
          )}
          <button type="submit" className="sign-in-button" disabled={submitting}>
            {submitting ? 'Signing in' : 'Sign in'}
          </button>
        </form>
      </div>
    </main>
  )
}
