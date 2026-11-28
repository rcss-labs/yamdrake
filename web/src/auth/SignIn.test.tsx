import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { adminMe, knownPassword } from '../test/fixtures'
import { renderApp } from '../test/render'
import { failNext, mockState, requests, server } from '../test/server'

function SignedInPage() {
  return <p>The signed-in page</p>
}

function renderSignIn(route = '/sign-in') {
  return renderApp(<SignedInPage />, { route, me: null })
}

async function fillAndSubmit(email: string, password: string) {
  const user = userEvent.setup()
  await user.type(await screen.findByLabelText('Email'), email)
  await user.type(screen.getByLabelText('Password'), password)
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  return user
}

// Waits until the methods request has been answered and rendered.
async function methodsAnswered() {
  await waitFor(() => {
    const call = requests.find((request) => request.path === '/api/v1/auth/methods')
    expect(call?.status).toBeDefined()
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20))
  })
}

describe('SignIn', () => {
  it('shows the product name and a labelled email and password form', async () => {
    renderSignIn()
    expect(await screen.findByText('Yumdrake')).toBeInTheDocument()
    expect(screen.getByLabelText('Email')).toHaveAttribute('type', 'email')
    expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'password')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
  })

  it('signs in and goes to the page the person came from', async () => {
    const { location } = renderSignIn('/sign-in?next=%2Fjobs%2F7%3Ftab%3Ddefinition')
    await fillAndSubmit(adminMe.email, knownPassword)
    expect(await screen.findByText('The signed-in page')).toBeInTheDocument()
    expect(location().pathname).toBe('/jobs/7')
    expect(location().search).toBe('?tab=definition')
  })

  it('goes to the start page when there is no next', async () => {
    const { location } = renderSignIn('/sign-in')
    await fillAndSubmit(adminMe.email, knownPassword)
    expect(await screen.findByText('The signed-in page')).toBeInTheDocument()
    expect(location().pathname).toBe('/')
  })

  it.each([['//evil.example'], ['https://evil.example'], ['/\\evil.example']])(
    'ignores the outside address %s in next',
    async (evil) => {
      const { location } = renderSignIn(`/sign-in?next=${encodeURIComponent(evil)}`)
      await fillAndSubmit(adminMe.email, knownPassword)
      expect(await screen.findByText('The signed-in page')).toBeInTheDocument()
      expect(location().pathname).toBe('/')
      expect(location().search).toBe('')
    },
  )

  it.each([
    ['rejected', 'Single sign-on did not complete. Try again, or ask an administrator to check the sign-in settings.'],
    ['refused', 'Your account may not sign in. Ask an administrator.'],
    ['unavailable', 'Single sign-on is not available right now. Sign in with your email and password.'],
    ['something-else', 'Single sign-on did not complete. Try again, or ask an administrator to check the sign-in settings.'],
  ])('explains the single sign-on failure %s above the form', async (code, text) => {
    renderSignIn(`/sign-in?sso_error=${code}`)
    expect(await screen.findByRole('alert')).toHaveTextContent(text)
    expect(screen.getByLabelText('Email')).toBeInTheDocument()
  })

  it('shows no single sign-on failure note without the sso_error parameter', async () => {
    renderSignIn()
    await screen.findByLabelText('Email')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('still returns to next after a single sign-on failure', async () => {
    const { location } = renderSignIn('/sign-in?sso_error=rejected&next=%2Fjobs%2F7')
    await fillAndSubmit(adminMe.email, knownPassword)
    expect(await screen.findByText('The signed-in page')).toBeInTheDocument()
    expect(location().pathname).toBe('/jobs/7')
  })

  it('posts the email and password to the login route', async () => {
    renderSignIn()
    await fillAndSubmit(adminMe.email, knownPassword)
    await screen.findByText('The signed-in page')
    const login = requests.find((request) => request.method === 'POST' && request.path === '/api/v1/auth/login')
    expect(login?.body).toEqual({ email: adminMe.email, password: knownPassword })
  })

  it('keeps the password out of the query cache after a failed and a successful sign-in', async () => {
    const { queryClient } = renderSignIn()
    const user = await fillAndSubmit(adminMe.email, 'not-the-password')
    await screen.findByRole('alert')
    const cached = () => JSON.stringify([queryClient.getMutationCache().getAll().map((m) => m.state), queryClient.getQueryCache().getAll().map((q) => q.state)])
    expect(cached()).not.toContain('not-the-password')
    await user.type(screen.getByLabelText('Password'), knownPassword)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('The signed-in page')
    expect(cached()).not.toContain(knownPassword)
  })

  it('says the email or password is not correct, keeps the email and empties the password', async () => {
    renderSignIn()
    await fillAndSubmit(adminMe.email, 'not-the-password')
    expect(await screen.findByRole('alert')).toHaveTextContent('Email or password is not correct')
    expect(screen.getByLabelText('Email')).toHaveValue(adminMe.email)
    expect(screen.getByLabelText('Password')).toHaveValue('')
    expect(screen.getByLabelText('Password')).toHaveFocus()
  })

  it('shows how long to wait when there are too many attempts', async () => {
    failNext('POST', '/api/v1/auth/login', 429, 'rate_limited', 'slow down', { 'Retry-After': '600' })
    renderSignIn()
    await fillAndSubmit(adminMe.email, knownPassword)
    expect(await screen.findByRole('alert')).toHaveTextContent('Too many attempts. Try again in 10 minutes')
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })

  it('rounds the wait up to whole minutes and uses the singular', async () => {
    failNext('POST', '/api/v1/auth/login', 429, 'rate_limited', 'slow down', { 'Retry-After': '61' })
    renderSignIn()
    await fillAndSubmit(adminMe.email, knownPassword)
    expect(await screen.findByRole('alert')).toHaveTextContent('Too many attempts. Try again in 2 minutes')
  })

  it('shows the server message when sign-in is unavailable', async () => {
    failNext('POST', '/api/v1/auth/login', 503, 'unavailable', 'Sign-in is temporarily unavailable')
    renderSignIn()
    await fillAndSubmit(adminMe.email, knownPassword)
    expect(await screen.findByRole('alert')).toHaveTextContent('Sign-in is temporarily unavailable')
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })

  it('says the server could not be reached when the network fails', async () => {
    server.use(http.post('/api/v1/auth/login', () => HttpResponse.error()))
    renderSignIn()
    await fillAndSubmit(adminMe.email, knownPassword)
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not reach the server/i)
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })

  it('offers company SSO when the server says SAML is enabled', async () => {
    renderSignIn()
    const link = await screen.findByRole('link', { name: 'Sign in with company SSO' })
    expect(link).toHaveAttribute('href', '/api/v1/auth/saml/start?next=%2F')
  })

  it('carries the page the person was heading to through company SSO', async () => {
    renderSignIn('/sign-in?next=%2Fcli%2Fconfirm%3Fcode%3DABCD-1234')
    const link = await screen.findByRole('link', { name: 'Sign in with company SSO' })
    expect(link).toHaveAttribute('href', '/api/v1/auth/saml/start?next=%2Fcli%2Fconfirm%3Fcode%3DABCD-1234')
  })

  it('does not pass an outside address through company SSO', async () => {
    renderSignIn(`/sign-in?next=${encodeURIComponent('//evil.example/x')}`)
    const link = await screen.findByRole('link', { name: 'Sign in with company SSO' })
    expect(link).toHaveAttribute('href', '/api/v1/auth/saml/start?next=%2F')
  })

  it('hides the SSO link when the methods route answers 404', async () => {
    failNext('GET', '/api/v1/auth/methods', 404, 'not_found', 'not found')
    renderSignIn()
    await screen.findByLabelText('Email')
    await methodsAnswered()
    expect(screen.queryByRole('link', { name: 'Sign in with company SSO' })).not.toBeInTheDocument()
  })

  it('hides the SSO link when SAML is not enabled', async () => {
    mockState().sso.enabled = false
    renderSignIn()
    await screen.findByLabelText('Email')
    await methodsAnswered()
    expect(screen.queryByRole('link', { name: 'Sign in with company SSO' })).not.toBeInTheDocument()
  })

  it('still shows the password form when the methods request fails', async () => {
    failNext('GET', '/api/v1/auth/methods', 500, 'internal', 'internal error')
    renderSignIn()
    expect(await screen.findByLabelText('Email')).toBeInTheDocument()
  })

  it('sends a person who is already signed in on to next', async () => {
    const { location } = renderApp(<SignedInPage />, { route: '/sign-in?next=%2Fjobs', me: adminMe })
    expect(await screen.findByText('The signed-in page')).toBeInTheDocument()
    expect(location().pathname).toBe('/jobs')
  })
})
