import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { failNext, mockState, requests } from '../../test/server'
import { renderApp } from '../../test/render'
import { RunsPage } from './RunsPage'

const runLinks = () => screen.queryAllByRole('link', { name: /^#\d+$/ }).map((link) => link.textContent)
const listRequests = () => requests.filter((request) => request.method === 'GET' && request.path === '/api/v1/runs')
const lastSearch = () => new URLSearchParams(listRequests().at(-1)?.search ?? '')

function addRuns(count: number) {
  const template = mockState().runs.find((run) => run.id === 109)!
  for (let index = 0; index < count; index += 1) {
    mockState().runs.push({ ...template, id: 200 + index })
  }
}

describe('the Runs page', () => {
  it('lists every run, newest first', async () => {
    renderApp(<RunsPage />, { route: '/runs' })
    expect(await screen.findByRole('heading', { level: 1, name: 'Runs' })).toBeInTheDocument()
    await screen.findByRole('link', { name: '#109' })
    expect(runLinks()).toEqual(['#109', '#108', '#107', '#106', '#105', '#104', '#103', '#102', '#101'])
    expect(screen.getByRole('columnheader', { name: 'Job' })).toBeInTheDocument()
  })

  it('filters by status: the segment asks for its states and is kept in the URL', async () => {
    const view = renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#109' })
    await userEvent.click(screen.getByRole('button', { name: 'Failed' }))
    await waitFor(() => expect(runLinks()).toEqual(['#106', '#105']))
    expect(lastSearch().getAll('state')).toEqual(['failed', 'partially_succeeded', 'needs_reconciliation'])
    expect(view.location().search).toBe('?status=failed')
    expect(screen.getByRole('button', { name: 'Failed' })).toHaveAttribute('aria-pressed', 'true')
    await userEvent.click(screen.getByRole('button', { name: 'Waiting' }))
    await waitFor(() => expect(runLinks()).toEqual(['#103', '#102', '#101']))
    expect(lastSearch().getAll('state')).toEqual(['planned', 'waiting_approval', 'resolving'])
    await userEvent.click(screen.getByRole('button', { name: 'Running' }))
    await waitFor(() => expect(runLinks()).toEqual(['#104']))
    expect(lastSearch().getAll('state')).toEqual(['queued', 'running', 'cancelling'])
    await userEvent.click(screen.getByRole('button', { name: 'Succeeded' }))
    await waitFor(() => expect(runLinks()).toEqual(['#109', '#108']))
    await userEvent.click(screen.getByRole('button', { name: 'All' }))
    await waitFor(() => expect(runLinks()).toHaveLength(9))
    expect(view.location().search).toBe('')
  })

  it('filters by environment and keeps it in the URL', async () => {
    const view = renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#109' })
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Filter by environment' }), 'preprod-01')
    await waitFor(() => expect(runLinks()).toEqual(['#106', '#101']))
    expect(lastSearch().get('environment')).toBe('preprod-01')
    expect(view.location().search).toBe('?environment=preprod-01')
  })

  it('searches by job, service or person and keeps the search in the URL', async () => {
    const view = renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#109' })
    await userEvent.type(screen.getByRole('searchbox', { name: 'Job, service or person' }), 'Omer')
    await waitFor(() => expect(runLinks()).toEqual(['#107', '#104', '#102']))
    expect(lastSearch().get('q')).toBe('Omer')
    expect(view.location().search).toBe('?q=Omer')
  })

  it('starts from the filters in the address', async () => {
    renderApp(<RunsPage />, { route: '/runs?status=succeeded&environment=hetzner-01&q=dana' })
    await screen.findByRole('link', { name: '#109' })
    expect(runLinks()).toEqual(['#109', '#108'])
    expect(screen.getByRole('button', { name: 'Succeeded' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('combobox', { name: 'Filter by environment' })).toHaveValue('hetzner-01')
    expect(screen.getByRole('searchbox', { name: 'Job, service or person' })).toHaveValue('dana')
  })

  it('says so when no run matches the filters', async () => {
    renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#109' })
    await userEvent.type(screen.getByRole('searchbox', { name: 'Job, service or person' }), 'nobody-here')
    expect(await screen.findByText('No runs match these filters')).toBeInTheDocument()
  })

  it('says so when nothing has run yet', async () => {
    mockState().runs = []
    renderApp(<RunsPage />, { route: '/runs' })
    expect(await screen.findByText('No runs yet')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('pages back with "Load older" using before, then stops offering it', async () => {
    addRuns(55)
    renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#254' })
    expect(runLinks()).toHaveLength(50)
    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(runLinks()).toHaveLength(64))
    const older = listRequests().find((request) => new URLSearchParams(request.search).has('before'))
    expect(new URLSearchParams(older?.search).get('before')).toBe('205')
    expect(runLinks()[0]).toBe('#254')
    expect(runLinks()[50]).toBe('#204')
    expect(runLinks()[55]).toBe('#109')
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('starts again from the newest page when a filter changes', async () => {
    addRuns(55)
    renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#254' })
    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(runLinks()).toHaveLength(64))
    await userEvent.click(screen.getByRole('button', { name: 'Succeeded' }))
    // 57 runs succeeded: only the newest page of them is shown, with Load older offered again.
    await waitFor(() => expect(runLinks()).toHaveLength(50))
    expect(screen.getByRole('button', { name: 'Load older' })).toBeInTheDocument()
    expect(listRequests().filter((request) => new URLSearchParams(request.search).has('before'))).toHaveLength(1)
  })

  it('loses and repeats no run when the first page is refreshed after new runs arrived', async () => {
    addRuns(55)
    const view = renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#254' })
    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(runLinks()).toHaveLength(64))
    const template = mockState().runs.find((run) => run.id === 109)!
    for (let id = 300; id < 305; id += 1) mockState().runs.push({ ...template, id })
    await view.queryClient.invalidateQueries({ queryKey: ['runs'] })
    await waitFor(() => expect(runLinks()).toHaveLength(69))
    const expected = [...mockState().runs.map((run) => run.id)].sort((a, b) => b - a).map((id) => `#${id}`)
    expect(runLinks()).toEqual(expected)
  })

  it('keeps the loaded runs and says so when an older page fails', async () => {
    addRuns(55)
    renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#254' })
    failNext('GET', '/api/v1/runs', 500, 'internal', 'Boom')
    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Older runs could not be loaded')
    expect(runLinks()).toHaveLength(50)
    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(runLinks()).toHaveLength(64))
  })

  it('waits for a pause in typing before searching, while the address follows every key', async () => {
    const view = renderApp(<RunsPage />, { route: '/runs' })
    await screen.findByRole('link', { name: '#109' })
    await userEvent.type(screen.getByRole('searchbox', { name: 'Job, service or person' }), 'Omer')
    expect(view.location().search).toBe('?q=Omer')
    await waitFor(() => expect(runLinks()).toEqual(['#107', '#104', '#102']))
    const asked = listRequests().map((request) => new URLSearchParams(request.search).get('q'))
    expect(asked.filter((q) => q !== null)).toEqual(['Omer'])
  })

  it('shows an error with a retry when the list cannot be loaded', async () => {
    failNext('GET', '/api/v1/runs', 500, 'internal')
    renderApp(<RunsPage />, { route: '/runs' })
    const alert = await screen.findByRole('alert')
    await userEvent.click(within(alert).getByRole('button', { name: /try again|retry/i }))
    expect(await screen.findByRole('link', { name: '#109' })).toBeInTheDocument()
  })
})
