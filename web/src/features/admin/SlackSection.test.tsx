import type { QueryClient } from '@tanstack/react-query'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { connectionsManagerMe, delayNext, mockState, requests } from '../../test/server'

const route = '/admin/slack'

function rememberedMutations(queryClient: QueryClient) {
  return JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => [mutation.state.variables, mutation.state.data]))
}

async function openSection() {
  const rendered = renderApp(<App />, { route, me: connectionsManagerMe })
  await screen.findByLabelText('Bot token')
  return rendered
}

describe('the Slack section', () => {
  it('says a token is set, since when and by whom, without showing it', async () => {
    await openSection()
    expect(await screen.findByText(/A token is set/)).toHaveTextContent('Dana Levi')
    expect(screen.getByLabelText('Bot token')).toHaveValue('')
  })

  it('says when no token is set', async () => {
    mockState().slack = { token_set: false, set_at: null, set_by_name: '' }
    await openSection()
    expect(await screen.findByText('No token is set')).toBeInTheDocument()
  })

  it('saves a new token, empties the field at once and keeps the token out of the page and the cache', async () => {
    delayNext('PUT', '/api/v1/admin/slack', 30)
    const { queryClient } = await openSection()
    const field = screen.getByLabelText('Bot token')
    expect(field).toHaveAttribute('type', 'password')
    await userEvent.type(field, 'xoxb-secret-value')
    await userEvent.click(screen.getByRole('button', { name: 'Save token' }))
    expect(screen.getByLabelText('Bot token')).toHaveValue('')
    await waitFor(() => expect(requests.find((request) => request.method === 'PUT' && request.path === '/api/v1/admin/slack')?.body).toEqual({ token: 'xoxb-secret-value' }))
    expect(await screen.findByText('Token saved')).toBeInTheDocument()
    expect(document.body.textContent).not.toContain('xoxb-secret-value')
    await waitFor(() => expect(rememberedMutations(queryClient)).not.toContain('xoxb-secret-value'))
  })

  it('does not save an empty token', async () => {
    await openSection()
    await userEvent.click(screen.getByRole('button', { name: 'Save token' }))
    expect(await screen.findByText('Paste the bot token first')).toBeInTheDocument()
    expect(requests.some((request) => request.method === 'PUT')).toBe(false)
  })

  it('shows that a test message was posted', async () => {
    await openSection()
    await userEvent.type(screen.getByLabelText('Channel'), '#deployments')
    await userEvent.click(screen.getByRole('button', { name: 'Send test message' }))
    expect(await screen.findByText('Slack accepted the test message')).toBeInTheDocument()
    expect(requests.find((request) => request.path === '/api/v1/admin/slack/test')?.body).toEqual({ channel: '#deployments' })
  })

  it('shows channel_not_found in words', async () => {
    await openSection()
    await userEvent.type(screen.getByLabelText('Channel'), '#missing')
    await userEvent.click(screen.getByRole('button', { name: 'Send test message' }))
    expect(await screen.findByText('Slack could not find that channel')).toBeInTheDocument()
  })

  it('shows a missing token in words', async () => {
    mockState().slack = { token_set: false, set_at: null, set_by_name: '' }
    await openSection()
    await userEvent.type(screen.getByLabelText('Channel'), '#deployments')
    await userEvent.click(screen.getByRole('button', { name: 'Send test message' }))
    expect(await screen.findByText('Set a token first')).toBeInTheDocument()
  })
})
