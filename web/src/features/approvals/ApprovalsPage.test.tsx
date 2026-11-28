import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { adminMe, failNext, jobManagerMe, mockState, requests, server } from '../../test/server'

const route = '/approvals'

describe('the approvals page', () => {
  it('shows who wants to run what where, and lets a person who may approve review it', async () => {
    renderApp(<App />, { route, me: adminMe })
    const card = await screen.findByRole('article', { name: /Yael Barak wants to run/ })
    expect(within(card).getByText('prod-01')).toBeInTheDocument()
    expect(within(card).getByText('prod')).toBeInTheDocument()
    expect(within(card).getByText(/Requested/)).toBeInTheDocument()
    expect(within(card).getByText(/Expires/)).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: 'Review and approve' })).toHaveAttribute('href', '/runs/103')
    expect(within(card).getByRole('link', { name: 'Details' })).toHaveAttribute('href', '/runs/103')
    expect(within(card).getByRole('button', { name: 'Reject' })).toBeInTheDocument()
  })

  it('says why a person may not approve and hides the buttons they cannot use', async () => {
    renderApp(<App />, { route, me: jobManagerMe })
    const card = await screen.findByRole('article', { name: /Yael Barak wants to run/ })
    expect(within(card).getByText('You do not have permission to approve in prod.')).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: 'Details' })).toBeInTheDocument()
    expect(within(card).queryByRole('link', { name: 'Review and approve' })).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument()
  })

  it('tells a person who started the run that someone else has to approve it', async () => {
    const approver = { ...jobManagerMe, grants: [{ action: 'approve' as const, label: '', environment: '', folder_id: 0 }] }
    renderApp(<App />, { route, me: approver })
    expect(await screen.findByText('You started this run, so someone else has to approve it.')).toBeInTheDocument()
  })

  it('renders the versions and the reason from the command-line tool\'s field names', async () => {
    server.use(
      http.get('/api/v1/approvals', () =>
        HttpResponse.json({
          approvals: [
            {
              run_id: 103,
              job_id: 'deploy-backends',
              environment: 'prod-01',
              requested_by_name: 'Yael Barak',
              requested_at: '2026-11-27T11:20:00Z',
              expires_at: '2026-11-27T15:20:00Z',
              versions: [{ service: 'backend-api', version: '1.9.4' }],
              can_approve: false,
              reason: 'You asked for this run.',
            },
          ],
        }),
      ),
    )
    renderApp(<App />, { route, me: adminMe })
    const card = await screen.findByRole('article', { name: /Yael Barak wants to run deploy-backends/ })
    expect(within(card).getByText(/backend-api 1\.9\.4/)).toBeInTheDocument()
    expect(within(card).getByText('You asked for this run.')).toBeInTheDocument()
  })

  it('still renders a row that has none of the web-only fields', async () => {
    server.use(
      http.get('/api/v1/approvals', () =>
        HttpResponse.json({
          approvals: [{ run_id: 103, job_id: 'deploy-backends', environment: 'prod-01', requested_by_name: 'Yael Barak', requested_at: '', expires_at: '', versions: [], can_approve: true, reason: '' }],
        }),
      ),
    )
    renderApp(<App />, { route, me: adminMe })
    const card = await screen.findByRole('article', { name: /Yael Barak wants to run deploy-backends/ })
    expect(within(card).getByRole('link', { name: 'Review and approve' })).toHaveAttribute('href', '/runs/103')
  })

  it('shows an empty state when nothing is waiting', async () => {
    const run = mockState().runs.find((candidate) => candidate.id === 103)
    if (run) run.state = 'queued'
    renderApp(<App />, { route, me: adminMe })
    expect(await screen.findByText('Nothing is waiting for approval')).toBeInTheDocument()
  })

  it('asks before rejecting, posts the rejection and drops the card', async () => {
    renderApp(<App />, { route, me: adminMe })
    await userEvent.click(await screen.findByRole('button', { name: 'Reject' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/will not start/)).toBeInTheDocument()
    expect(requests.some((request) => request.path === '/api/v1/runs/103/reject')).toBe(false)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Reject run' }))
    await waitFor(() => expect(requests.some((request) => request.method === 'POST' && request.path === '/api/v1/runs/103/reject')).toBe(true))
    expect(await screen.findByText('Nothing is waiting for approval')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('closes the dialog without rejecting when the person goes back', async () => {
    renderApp(<App />, { route, me: adminMe })
    await userEvent.click(await screen.findByRole('button', { name: 'Reject' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Go back' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(requests.some((request) => request.path === '/api/v1/runs/103/reject')).toBe(false)
  })

  it('shows the server message when the run was already decided, and closes the dialog', async () => {
    failNext('POST', '/api/v1/runs/103/reject', 409, 'conflict', 'This run was already decided.')
    renderApp(<App />, { route, me: adminMe })
    await userEvent.click(await screen.findByRole('button', { name: 'Reject' }))
    // Someone else decided it meanwhile.
    const run = mockState().runs.find((candidate) => candidate.id === 103)
    if (run) run.state = 'queued'
    await userEvent.click(await screen.findByRole('button', { name: 'Reject run' }))
    expect(await screen.findByText('This run was already decided.')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(await screen.findByText('Nothing is waiting for approval')).toBeInTheDocument()
    expect(screen.queryByRole('article')).not.toBeInTheDocument()
  })
})
