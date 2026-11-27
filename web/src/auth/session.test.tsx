import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { adminMe, jobManagerMe, knownPassword, viewerMe } from '../test/fixtures'
import { renderApp } from '../test/render'
import { failNext, requests, server } from '../test/server'
import { useMe, useSignOut } from './session'

function WhoAmI() {
  const me = useMe()
  return <p>Signed in as {me.name}</p>
}

function CreateFolderForm() {
  const [message, setMessage] = useState('')
  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setMessage('')
    try {
      await api('POST', '/api/v1/folders', { name: 'Tools' })
      setMessage('Folder created')
    } catch {
      setMessage('Folder not created')
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)}>
      <p>Create a folder</p>
      <button type="submit">Create folder</button>
      <p role="status">{message}</p>
    </form>
  )
}

function SignOutButton() {
  const signOut = useSignOut()
  return <button onClick={() => void signOut()}>Sign out</button>
}

const meRequests = () => requests.filter((request) => request.method === 'GET' && request.path === '/api/v1/me')

describe('SessionProvider', () => {
  it('shows a quiet loading line, then the page for the signed-in person', async () => {
    renderApp(<WhoAmI />, { me: jobManagerMe })
    expect(screen.getByRole('status')).toHaveTextContent(/loading/i)
    expect(await screen.findByText(`Signed in as ${jobManagerMe.name}`)).toBeInTheDocument()
  })

  it('loads /me once however often the tree renders', async () => {
    const { rerender } = renderApp(<WhoAmI />, { me: adminMe })
    await screen.findByText(`Signed in as ${adminMe.name}`)
    rerender(<WhoAmI />)
    await screen.findByText(`Signed in as ${adminMe.name}`)
    expect(meRequests()).toHaveLength(1)
  })

  it('shows sign-in with the current path as next when nobody is signed in', async () => {
    const { location } = renderApp(<WhoAmI />, { route: '/jobs/7?tab=definition', me: null })
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.getByText('Yumdrake')).toBeInTheDocument()
    expect(location().pathname).toBe('/sign-in')
    expect(new URLSearchParams(location().search).get('next')).toBe('/jobs/7?tab=definition')
    expect(screen.queryByText(/Signed in as/)).not.toBeInTheDocument()
  })

  it('does not add next when the person was on the start page', async () => {
    const { location } = renderApp(<WhoAmI />, { route: '/', me: null })
    await screen.findByRole('button', { name: 'Sign in' })
    expect(location().search).toBe('')
  })

  it.each([
    ['a server error', () => failNext('GET', '/api/v1/me', 500, 'internal', 'internal error')],
    ['a network failure', () => server.use(http.get('/api/v1/me', () => HttpResponse.error()))],
  ])('shows a readable error with a retry button after %s, never sign-in or a blank page', async (_name, breakIt) => {
    breakIt()
    renderApp(<WhoAmI />, { me: adminMe })
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/could not load your session/i)
    expect(screen.queryByRole('button', { name: 'Sign in' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('loads the session when Try again is pressed after a failure', async () => {
    failNext('GET', '/api/v1/me', 503, 'unavailable', 'Sign-in is temporarily unavailable')
    renderApp(<WhoAmI />, { me: adminMe })
    await screen.findByRole('alert')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText(`Signed in as ${adminMe.name}`)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('does not wait forever: a 15 second timeout would end in the same error, shown by the client', async () => {
    // The timeout itself is tested in client.test.ts; here the error body of a gateway page is enough.
    server.use(http.get('/api/v1/me', () => new HttpResponse('<html>Bad gateway</html>', { status: 502 })))
    renderApp(<WhoAmI />, { me: adminMe })
    expect(await screen.findByRole('alert')).toBeInTheDocument()
  })

  // Review Focus 1: the session expires while a page is open.
  it('sends the person to sign-in and back to the same page when a later request is answered with 401', async () => {
    const { location } = renderApp(<CreateFolderForm />, { route: '/jobs/7', me: jobManagerMe })
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Create folder' }))
    // Sign the mock server out first: the request the person's click makes finds the session gone.
    failNext('POST', '/api/v1/folders', 401, 'unauthorized', 'Sign in again')
    await user.click(await screen.findByRole('button', { name: 'Create folder' }))

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(location().pathname).toBe('/sign-in')
    expect(new URLSearchParams(location().search).get('next')).toBe('/jobs/7')
    expect(screen.queryByText('Create a folder')).not.toBeInTheDocument()

    await user.type(screen.getByLabelText('Email'), jobManagerMe.email)
    await user.type(screen.getByLabelText('Password'), knownPassword)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByText('Create a folder')).toBeInTheDocument()
    expect(location().pathname).toBe('/jobs/7')
    // Nothing was sent again behind the person's back: two clicks, two posts, and none after sign-in.
    expect(requests.filter((request) => request.method === 'POST' && request.path === '/api/v1/folders')).toHaveLength(2)
    expect(screen.getByRole('status')).toHaveTextContent('')
  })

  it('does not replay a refused submission after the person signs in again', async () => {
    renderApp(<CreateFolderForm />, { route: '/jobs/7', me: jobManagerMe })
    const user = userEvent.setup()
    await screen.findByText('Create a folder')
    failNext('POST', '/api/v1/folders', 401, 'unauthorized', 'Sign in again')
    await user.click(screen.getByRole('button', { name: 'Create folder' }))
    await screen.findByRole('button', { name: 'Sign in' })
    await user.type(screen.getByLabelText('Email'), jobManagerMe.email)
    await user.type(screen.getByLabelText('Password'), knownPassword)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('Create a folder')
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(requests.filter((request) => request.method === 'POST' && request.path === '/api/v1/folders')).toHaveLength(1)
  })

  it('signs out through the logout route and shows sign-in', async () => {
    renderApp(<SignOutButton />, { route: '/jobs', me: viewerMe })
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign out' }))
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(requests.some((request) => request.method === 'POST' && request.path === '/api/v1/auth/logout')).toBe(true)
    // The server no longer knows the person.
    await expect(api('GET', '/api/v1/me')).rejects.toMatchObject({ status: 401 })
  })

  it('throws when useMe is used outside a signed-in tree', async () => {
    const { render } = await import('@testing-library/react')
    const quiet = () => undefined
    const spy = vi.spyOn(console, 'error').mockImplementation(quiet)
    expect(() => render(<WhoAmI />)).toThrow(/signed-in/i)
    spy.mockRestore()
  })

  it('forgets what the previous person could see when the session ends', async () => {
    renderApp(<WhoAmI />, { me: adminMe })
    await screen.findByText(`Signed in as ${adminMe.name}`)
    failNext('GET', '/api/v1/folders', 401, 'unauthorized', 'Sign in again')
    await api('GET', '/api/v1/folders').catch(() => undefined)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument())
  })
})
