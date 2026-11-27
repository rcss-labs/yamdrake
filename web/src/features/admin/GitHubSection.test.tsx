import type { QueryClient } from '@tanstack/react-query'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { connectionsManagerMe, delayNext, failNext, mockState, requests, viewerMe } from '../../test/server'

const route = '/admin/github'
const path = '/api/v1/admin/github-connections'
const pem = '-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----'

async function openForm() {
  const rendered = renderApp(<App />, { route, me: connectionsManagerMe })
  await screen.findByRole('table', { name: 'GitHub connections' })
  await userEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
  return rendered
}

/** Every mutation the query client still remembers, with its variables and data, as one string. */
function rememberedMutations(queryClient: QueryClient) {
  return JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => [mutation.state.variables, mutation.state.data]))
}

describe('the GitHub section', () => {
  it('lists connections with their method and details but no secrets', async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    const app = await screen.findByRole('row', { name: /example-org/ })
    expect(within(app).getByText('GitHub App')).toBeInTheDocument()
    expect(within(app).getByText('App 123456, installation 7890123')).toBeInTheDocument()
    const token = screen.getByRole('row', { name: /example-labs/ })
    expect(within(token).getByText('Access token')).toBeInTheDocument()
    expect(within(token).getByText('Stored encrypted')).toBeInTheDocument()
  })

  it('shows an empty state that says how to connect', async () => {
    mockState().githubConnections.length = 0
    renderApp(<App />, { route, me: connectionsManagerMe })
    expect(await screen.findByText('No GitHub connections')).toBeInTheDocument()
    expect(screen.getByText(/Connect an organisation or owner/)).toBeInTheDocument()
  })

  it('shows loading, then an error with a retry', async () => {
    delayNext('GET', path, 50)
    failNext('GET', path, 500, 'internal', 'The database is down')
    renderApp(<App />, { route, me: connectionsManagerMe })
    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(await screen.findByText('The database is down')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table', { name: 'GitHub connections' })).toBeInTheDocument()
  })

  it('connects by token and clears the token afterwards', async () => {
    delayNext('POST', path, 30)
    const { queryClient } = await openForm()
    await userEvent.type(screen.getByLabelText('Organisation or owner'), 'new-org')
    await userEvent.selectOptions(screen.getByLabelText('Access method'), 'token')
    const token = screen.getByLabelText('Access token')
    expect(token).toHaveAttribute('type', 'password')
    await userEvent.type(token, 'ghp_secretvalue')
    await userEvent.click(screen.getByRole('button', { name: 'Save connection' }))
    // While the request is pending the form is still mounted and the token is already empty.
    expect(screen.getByLabelText('Access token')).toHaveValue('')
    expect(await screen.findByRole('row', { name: /new-org/ })).toBeInTheDocument()
    expect(requests.find((request) => request.method === 'POST' && request.path === path)?.body).toEqual({
      owner: 'new-org',
      method: 'token',
      token: 'ghp_secretvalue',
    })
    await waitFor(() => expect(rememberedMutations(queryClient)).not.toContain('ghp_secretvalue'))
  })

  it('connects by GitHub App with the private key as a secret field', async () => {
    await openForm()
    await userEvent.type(screen.getByLabelText('Organisation or owner'), 'app-org')
    expect(screen.getByLabelText('Access method')).toHaveValue('app')
    await userEvent.type(screen.getByLabelText('App ID'), '42')
    await userEvent.type(screen.getByLabelText('Installation ID'), '99')
    await userEvent.type(screen.getByLabelText('Private key'), pem)
    await userEvent.click(screen.getByRole('button', { name: 'Save connection' }))
    const row = await screen.findByRole('row', { name: /app-org/ })
    expect(within(row).getByText('App 42, installation 99')).toBeInTheDocument()
    expect(requests.find((request) => request.method === 'POST' && request.path === path)?.body).toEqual({
      owner: 'app-org',
      method: 'app',
      app_id: 42,
      installation_id: 99,
      private_key: pem,
    })
  })

  it('shows a server validation error on its field and clears secrets after a failure', async () => {
    const { queryClient } = await openForm()
    await userEvent.type(screen.getByLabelText('Organisation or owner'), '-bad')
    await userEvent.type(screen.getByLabelText('App ID'), '42')
    await userEvent.type(screen.getByLabelText('Installation ID'), '99')
    await userEvent.type(screen.getByLabelText('Private key'), pem)
    await userEvent.click(screen.getByRole('button', { name: 'Save connection' }))
    const owner = screen.getByLabelText('Organisation or owner')
    expect(await screen.findByText(/is not a valid GitHub user or organisation name/)).toBeInTheDocument()
    expect(owner).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText('Private key')).toHaveValue('')
    expect(rememberedMutations(queryClient)).not.toContain('BEGIN RSA')
  })

  it('shows a message that names several App fields on the form, not on one field', async () => {
    await openForm()
    await userEvent.type(screen.getByLabelText('Organisation or owner'), 'app-org')
    await userEvent.click(screen.getByRole('button', { name: 'Save connection' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/requires app_id, installation_id and private_key/)
    expect(screen.getByLabelText('App ID')).not.toHaveAttribute('aria-invalid')
  })

  it('shows a duplicate owner in plain words', async () => {
    await openForm()
    await userEvent.type(screen.getByLabelText('Organisation or owner'), 'example-labs')
    await userEvent.selectOptions(screen.getByLabelText('Access method'), 'token')
    await userEvent.type(screen.getByLabelText('Access token'), 'ghp_x')
    await userEvent.click(screen.getByRole('button', { name: 'Save connection' }))
    expect(await screen.findByText(/already has a connection/)).toBeInTheDocument()
  })

  it('shows a 403 from the server as a clear message', async () => {
    await openForm()
    failNext('POST', path, 403, 'forbidden', 'You do not have permission to do this')
    await userEvent.type(screen.getByLabelText('Organisation or owner'), 'new-org')
    await userEvent.selectOptions(screen.getByLabelText('Access method'), 'token')
    await userEvent.type(screen.getByLabelText('Access token'), 'ghp_x')
    await userEvent.click(screen.getByRole('button', { name: 'Save connection' }))
    expect(await screen.findByText('You do not have permission to do this')).toBeInTheDocument()
  })

  it('has no tab and makes no admin request for a person without manage_connections', async () => {
    renderApp(<App />, { route, me: viewerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.queryByRole('tab', { name: 'GitHub' })).not.toBeInTheDocument()
    expect(requests.filter((request) => request.path.startsWith('/api/v1/admin/'))).toEqual([])
  })
})
