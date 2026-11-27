import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { connectionsManagerMe, delayNext, failNext, jobsFixture, mockState, requests, viewerMe } from '../../test/server'

const route = '/admin/sources'

async function fillSource(repo: string, branch: string, path: string) {
  await userEvent.type(screen.getByLabelText('Repository'), repo)
  await userEvent.type(screen.getByLabelText('Branch'), branch)
  await userEvent.type(screen.getByLabelText('Path'), path)
}

describe('the Git sources section', () => {
  it('leaves the inventory source empty, explains that saving replaces it and shows the active commit', async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    expect(await screen.findByText(/a1b2c3d4e5f6/)).toBeInTheDocument()
    expect(screen.getByLabelText('Repository')).toHaveValue('')
    expect(screen.getByLabelText('Branch')).toHaveValue('')
    expect(screen.getByLabelText('Path')).toHaveValue('')
    expect(screen.getByText(/Saving replaces the current inventory source and reads it again/)).toBeInTheDocument()
  })

  it('saves the source and shows the result of the first read', async () => {
    mockState().inventory.rejected = null
    renderApp(<App />, { route, me: connectionsManagerMe })
    await screen.findByText(/a1b2c3d4e5f6/)
    await fillSource('example-org/new-config', 'main', 'inventory')
    await userEvent.click(screen.getByRole('button', { name: 'Save and re-read' }))
    expect(await screen.findByText(/Saved. Yumdrake re-read the repository/)).toBeInTheDocument()
    expect(requests.find((request) => request.method === 'PUT' && request.path === '/api/v1/admin/inventory-source')?.body).toEqual({
      repo: 'example-org/new-config',
      branch: 'main',
      path: 'inventory',
    })
    expect(mockState().inventorySource.repo).toBe('example-org/new-config')
  })

  it('shows each problem of a rejected commit with its file and line, and that the last good version stays in use', async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    await screen.findByText(/a1b2c3d4e5f6/)
    await fillSource('example-org/new-config', 'main', 'inventory')
    await userEvent.click(screen.getByRole('button', { name: 'Save and re-read' }))
    expect(await screen.findByText('services/billing.yaml:14')).toBeInTheDocument()
    expect(screen.getByText('ecs_service is required')).toBeInTheDocument()
    expect(screen.getByText(/last good version stays in use/i)).toBeInTheDocument()
  })

  it('shows a sync error from the save', async () => {
    mockState().inventory.sync_error = 'repository not found'
    mockState().inventory.rejected = null
    renderApp(<App />, { route, me: connectionsManagerMe })
    await screen.findByText(/a1b2c3d4e5f6/)
    await fillSource('example-org/new-config', 'main', 'inventory')
    await userEvent.click(screen.getByRole('button', { name: 'Save and re-read' }))
    expect(await screen.findByText(/repository not found/)).toBeInTheDocument()
  })

  it('shows a server validation error on its field', async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    await screen.findByText(/a1b2c3d4e5f6/)
    await fillSource('not-a-repo', 'main', 'inventory')
    await userEvent.click(screen.getByRole('button', { name: 'Save and re-read' }))
    const repo = screen.getByLabelText('Repository')
    expect(await screen.findByText('repo "not-a-repo" must look like owner/name')).toBeInTheDocument()
    expect(repo).toHaveAttribute('aria-invalid', 'true')
  })

  it('shows a 403 from the server as a clear message', async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    await screen.findByText(/a1b2c3d4e5f6/)
    failNext('PUT', '/api/v1/admin/inventory-source', 403, 'forbidden', 'You do not have permission to do this')
    await fillSource('example-org/new-config', 'main', 'inventory')
    await userEvent.click(screen.getByRole('button', { name: 'Save and re-read' }))
    expect(await screen.findByText('You do not have permission to do this')).toBeInTheDocument()
  })

  it("lists every job's source with the repository, branch and path", async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    const table = await screen.findByRole('table', { name: 'Job sources' })
    const first = jobsFixture[0]!
    const row = within(table).getByRole('row', { name: new RegExp(first.title) })
    expect(within(row).getByText(first.repo)).toBeInTheDocument()
    expect(within(row).getByText(first.path)).toBeInTheDocument()
    expect(within(table).getAllByRole('row')).toHaveLength(jobsFixture.length + 1)
  })

  it('says there are no jobs yet and where to add them', async () => {
    mockState().jobs.length = 0
    renderApp(<App />, { route, me: connectionsManagerMe })
    expect(await screen.findByText('No jobs have a source yet')).toBeInTheDocument()
    expect(screen.getByText(/Register a job on the Jobs page/)).toBeInTheDocument()
  })

  it('shows loading, then an error with a retry for the job sources', async () => {
    delayNext('GET', '/api/v1/jobs', 50)
    failNext('GET', '/api/v1/jobs', 500, 'internal', 'The database is down')
    renderApp(<App />, { route, me: connectionsManagerMe })
    expect(await screen.findByText('The database is down')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table', { name: 'Job sources' })).toBeInTheDocument()
  })

  it('has no tab and requests nothing for a person without manage_connections', async () => {
    renderApp(<App />, { route, me: viewerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.queryByRole('tab', { name: 'Git sources' })).not.toBeInTheDocument()
    expect(requests.filter((request) => request.path.startsWith('/api/v1/admin/'))).toEqual([])
  })
})
