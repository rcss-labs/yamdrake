import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { delayNext, failNext, mockState, requests, viewerMe } from '../../test/server'

const tokenRequests = (method: string) => requests.filter((request) => request.method === method && request.path.startsWith('/api/v1/tokens'))

afterEach(() => {
  vi.restoreAllMocks()
})

async function createToken(name: string) {
  await userEvent.type(await screen.findByLabelText('Token name'), name)
  await userEvent.click(screen.getByRole('button', { name: 'Create token' }))
}

describe('the CLI tokens page', () => {
  it('shows the CLI usage text and the person\'s tokens', async () => {
    renderApp(<App />, { route: '/cli' })
    const usage = await screen.findByRole('region', { name: 'CLI usage' })
    expect(usage).toHaveTextContent('yumdrake login --sso')
    expect(usage).toHaveTextContent('yumdrake validate ./yumdrake-config')
    const table = await screen.findByRole('table', { name: 'Your CLI tokens' })
    expect(within(table).getAllByRole('columnheader').map((header) => header.textContent)).toEqual([
      'Name',
      'Created',
      'Last used',
      'Expires',
      'Actions',
    ])
    const row = within(table).getByText('laptop cli').closest('tr')!
    expect(row).not.toHaveTextContent('Never')
    expect(within(row).getByRole('button', { name: 'Revoke laptop cli' })).toBeInTheDocument()
  })

  it('says Never for a token that was never used', async () => {
    mockState().tokens[1] = [{ id: 7, name: 'ci', created_at: '2026-11-01T10:00:00Z', expires_at: '2027-01-30T10:00:00Z', last_used_at: null }]
    renderApp(<App />, { route: '/cli' })
    const row = (await screen.findByText('ci')).closest('tr')!
    expect(within(row).getByText('Never')).toBeInTheDocument()
  })

  it('has an empty state that says how to make a token', async () => {
    mockState().tokens[1] = []
    renderApp(<App />, { route: '/cli' })
    expect(await screen.findByText('You have no CLI tokens')).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: 'Your CLI tokens' })).not.toBeInTheDocument()
  })

  it('shows a loading state, then an error with retry', async () => {
    delayNext('GET', '/api/v1/tokens', 200)
    renderApp(<App />, { route: '/cli' })
    expect(await screen.findByRole('status')).toBeInTheDocument()
    await screen.findByRole('table', { name: 'Your CLI tokens' })
  })

  it('shows an error with retry when tokens cannot be loaded', async () => {
    failNext('GET', '/api/v1/tokens', 500, 'internal', 'Tokens are unavailable.')
    renderApp(<App />, { route: '/cli' })
    expect(await screen.findByRole('alert')).toHaveTextContent('Tokens are unavailable.')
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table', { name: 'Your CLI tokens' })).toBeInTheDocument()
  })

  it('creates a token, shows the value once and lists the new token', async () => {
    renderApp(<App />, { route: '/cli' })
    await userEvent.selectOptions(await screen.findByLabelText('Expires after'), '30')
    await createToken('build box')

    const post = tokenRequests('POST')[0]
    expect(post?.body).toEqual({ name: 'build box', ttl_days: 30 })
    const value = await screen.findByTestId('new-token-value')
    expect(value).toHaveTextContent(/^ymd_mock/)
    expect(value).toHaveClass('mono')
    expect(screen.getByText('This is the only time the token is shown')).toBeInTheDocument()
    expect(await within(screen.getByRole('table', { name: 'Your CLI tokens' })).findByText('build box')).toBeInTheDocument()
    expect(screen.getByLabelText('Token name')).toHaveValue('')
  })

  it('copies the value, and says so when copying is not possible', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    renderApp(<App />, { route: '/cli' })
    await createToken('one')
    const value = (await screen.findByTestId('new-token-value')).textContent
    await userEvent.click(screen.getByRole('button', { name: 'Copy token' }))
    expect(writeText).toHaveBeenCalledWith(value)
    expect(await screen.findByText('Copied')).toBeInTheDocument()

    writeText.mockRejectedValue(new Error('denied'))
    await userEvent.click(screen.getByRole('button', { name: 'Copy token' }))
    expect(await screen.findByText('Could not copy. Select the value and copy it by hand.')).toBeInTheDocument()
  })

  it('does not keep the value after leaving the page and coming back', async () => {
    renderApp(<App />, { route: '/cli' })
    await createToken('gone soon')
    await screen.findByTestId('new-token-value')

    await userEvent.click(screen.getByRole('link', { name: 'Jobs' }))
    await screen.findByRole('heading', { name: 'Jobs' })
    await userEvent.click(screen.getByRole('link', { name: 'CLI' }))

    await screen.findByRole('table', { name: 'Your CLI tokens' })
    expect(screen.queryByTestId('new-token-value')).not.toBeInTheDocument()
    expect(screen.queryByText('This is the only time the token is shown')).not.toBeInTheDocument()
    expect(document.body.textContent).not.toContain('ymd_mock')
  })

  it('leaves no trace of the value in any query or mutation cache after leaving', async () => {
    const { queryClient } = renderApp(<App />, { route: '/cli' })
    await createToken('cache check')
    await screen.findByTestId('new-token-value')
    await screen.findByText('cache check', { selector: 'td *, td' })

    await userEvent.click(screen.getByRole('link', { name: 'Jobs' }))
    await screen.findByRole('heading', { name: 'Jobs' })

    await waitFor(() => {
      const mutations = queryClient.getMutationCache().getAll().map((mutation) => JSON.stringify(mutation.state))
      expect(mutations.join('')).not.toContain('ymd_mock')
    })
    const queries = queryClient.getQueryCache().getAll().map((query) => JSON.stringify(query.state.data ?? null))
    expect(queries.join('')).not.toContain('ymd_mock')
  })

  it('asks for a name before sending anything', async () => {
    renderApp(<App />, { route: '/cli' })
    await userEvent.click(await screen.findByRole('button', { name: 'Create token' }))
    expect(await screen.findByText('Enter a name for the token')).toBeInTheDocument()
    expect(tokenRequests('POST')).toEqual([])
  })

  it('shows a server error next to the form', async () => {
    renderApp(<App />, { route: '/cli' })
    await screen.findByRole('table', { name: 'Your CLI tokens' })
    failNext('POST', '/api/v1/tokens', 500, 'internal', 'Could not create the token.')
    await createToken('x')
    expect(await screen.findByText('Could not create the token.')).toBeInTheDocument()
    expect(screen.queryByTestId('new-token-value')).not.toBeInTheDocument()
  })

  it('revokes only after confirmation, then refreshes the list', async () => {
    renderApp(<App />, { route: '/cli' })
    await userEvent.click(await screen.findByRole('button', { name: 'Revoke laptop cli' }))
    const dialog = await screen.findByRole('group', { name: 'Revoke laptop cli?' })
    expect(dialog).toHaveTextContent('Anything using this token stops working')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(tokenRequests('DELETE')).toEqual([])
    expect(screen.getByText('laptop cli')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Revoke laptop cli' }))
    await userEvent.click(within(await screen.findByRole('group', { name: 'Revoke laptop cli?' })).getByRole('button', { name: 'Revoke token' }))
    await waitFor(() => expect(tokenRequests('DELETE')).toHaveLength(1))
    expect(tokenRequests('DELETE')[0]?.path).toBe('/api/v1/tokens/1')
    expect(await screen.findByText('You have no CLI tokens')).toBeInTheDocument()
  })

  it('works for a person with no grants', async () => {
    renderApp(<App />, { route: '/cli', me: viewerMe })
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeInTheDocument()
  })
})
