import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { accessManagerMe, mockState, requests } from '../../test/server'
import { mockNow } from '../../test/fixtures'

const route = '/admin/freezes'
const createPath = '/api/v1/admin/freezes'

beforeEach(() => {
  // The mock server judges expiry against its fixed clock, so the page's clock must agree with it.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(mockNow))
})
afterEach(() => vi.useRealTimers())

async function openSection() {
  renderApp(<App />, { route, me: accessManagerMe })
  await screen.findByRole('table', { name: 'Active freezes' })
}

const posts = () => requests.filter((request) => request.method === 'POST' && request.path === createPath)

describe('the freezes section', () => {
  it('lists active freezes with scope, reason, who and until when, and recent ended ones', async () => {
    await openSection()
    const active = screen.getByRole('row', { name: /Quarter-end change freeze/ })
    expect(within(active).getByText('prod')).toBeInTheDocument()
    expect(within(active).getByText('Dana Levi')).toBeInTheDocument()
    expect(within(active).getByRole('button', { name: /^Lift/ })).toBeInTheDocument()
    const ended = screen.getByRole('row', { name: /Database migration/ })
    expect(within(ended).getByText('preprod-01')).toBeInTheDocument()
    expect(within(ended).queryByRole('button', { name: /^Lift/ })).not.toBeInTheDocument()
  })

  it('needs a reason before it posts anything', async () => {
    await openSection()
    await userEvent.selectOptions(screen.getByLabelText('Freeze'), 'environment')
    await userEvent.selectOptions(screen.getByLabelText('Environment'), 'hetzner-01')
    await userEvent.click(screen.getByRole('button', { name: 'Freeze deployments' }))
    expect(await screen.findByText('Give a reason so others know why')).toBeInTheDocument()
    expect(posts()).toHaveLength(0)
  })

  it('refuses a custom length of more than 30 days', async () => {
    await openSection()
    await screen.findByRole('option', { name: 'dev' })
    await userEvent.selectOptions(screen.getByLabelText('Label'), 'dev')
    await userEvent.type(screen.getByLabelText('Reason'), 'Holiday')
    await userEvent.selectOptions(screen.getByLabelText('Length'), 'custom')
    await userEvent.type(screen.getByLabelText('Days'), '31')
    await userEvent.click(screen.getByRole('button', { name: 'Freeze deployments' }))
    expect(await screen.findByText('A freeze lasts between 1 and 30 days')).toBeInTheDocument()
    expect(posts()).toHaveLength(0)
  })

  it('freezes an environment for a preset length and clears the form', async () => {
    await openSection()
    await userEvent.selectOptions(screen.getByLabelText('Freeze'), 'environment')
    await userEvent.selectOptions(screen.getByLabelText('Environment'), 'hetzner-01')
    await userEvent.type(screen.getByLabelText('Reason'), 'Disk swap')
    await userEvent.selectOptions(screen.getByLabelText('Length'), '4h')
    await userEvent.click(screen.getByRole('button', { name: 'Freeze deployments' }))
    expect(await screen.findByRole('row', { name: /Disk swap/ })).toBeInTheDocument()
    expect(posts()[0]?.body).toEqual({ environment: 'hetzner-01', reason: 'Disk swap', expires_at: '2026-11-27T16:00:00.000Z' })
    expect(screen.getByLabelText('Reason')).toHaveValue('')
  })

  it('freezes a label for a custom number of days', async () => {
    await openSection()
    await screen.findByRole('option', { name: 'dev' })
    await userEvent.selectOptions(screen.getByLabelText('Label'), 'dev')
    await userEvent.type(screen.getByLabelText('Reason'), 'Holiday')
    await userEvent.selectOptions(screen.getByLabelText('Length'), 'custom')
    await userEvent.type(screen.getByLabelText('Days'), '30')
    await userEvent.click(screen.getByRole('button', { name: 'Freeze deployments' }))
    await waitFor(() => expect(posts()).toHaveLength(1))
    expect(posts()[0]?.body).toEqual({ label: 'dev', reason: 'Holiday', expires_at: '2026-12-27T11:59:00.000Z' })
  })

  it('asks before lifting a freeze, then moves it to the ended list', async () => {
    await openSection()
    await userEvent.click(within(screen.getByRole('row', { name: /Quarter-end change freeze/ })).getByRole('button', { name: /^Lift/ }))
    const dialog = await screen.findByRole('dialog')
    expect(requests.some((request) => request.method === 'DELETE')).toBe(false)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Lift freeze' }))
    await waitFor(() => expect(requests.some((request) => request.method === 'DELETE' && request.path === '/api/v1/admin/freezes/1')).toBe(true))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(mockState().freezes.find((freeze) => freeze.id === 1)?.ended_at).not.toBeNull()
    const ended = within(await screen.findByRole('table', { name: 'Ended freezes' }))
    expect(await ended.findByText('Quarter-end change freeze')).toBeInTheDocument()
  })
})
