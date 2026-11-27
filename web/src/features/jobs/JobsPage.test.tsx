import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { Route, Routes } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { adminMe, delayNext, failNext, jobManagerMe, mockState, requests, server, viewerMe } from '../../test/server'
import { renderApp } from '../../test/render'
import { JobsPage } from './JobsPage'

function renderJobs(me = adminMe) {
  return renderApp(
    <Routes>
      <Route path="/jobs" element={<JobsPage />} />
      <Route path="/jobs/:id" element={<p>Job page opened</p>} />
    </Routes>,
    { route: '/jobs', me },
  )
}

// A folder's heading row is the one whose single cell spans the table.
const groupRows = () => screen.queryAllByRole('row').filter((row) => row.querySelector('td[colspan]'))
const groupRow = (name: string) => {
  const found = groupRows().find((row) => row.textContent?.startsWith(name))
  if (!found) throw new Error(`No group row for ${name}`)
  return found
}
const jobRowOf = (title: string) => screen.getByRole('link', { name: title }).closest('tr') as HTMLElement

describe('JobsPage', () => {
  it('groups jobs under their folders with counts and a count line', async () => {
    renderJobs()
    expect(await screen.findByRole('link', { name: 'Backend and microservices' })).toBeInTheDocument()
    expect(screen.getByText(/4 jobs in 3 folders/)).toBeInTheDocument()
    expect(groupRow('Backend')).toHaveTextContent('2 jobs')
    expect(groupRow('Portal')).toHaveTextContent('1 job')
    expect(groupRow('Releases')).toHaveTextContent('1 job')
    const backend = groupRow('Backend')
    expect(backend.nextElementSibling).toHaveTextContent('Backend and microservices')
    expect(within(jobRowOf('Backend and microservices')).getByText('deploy-backend')).toBeInTheDocument()
  })

  it('links each job to its page', async () => {
    renderJobs()
    expect(await screen.findByRole('link', { name: 'Portal services' })).toHaveAttribute('href', '/jobs/3')
  })

  it('shows deploys, approval, state and last run for each job', async () => {
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    const backend = within(jobRowOf('Backend and microservices'))
    expect(backend.getByText('backend-api')).toBeInTheDocument()
    expect(backend.getByText('prod')).toBeInTheDocument()
    expect(backend.getByText('Runnable')).toBeInTheDocument()
    expect(backend.getByText('No runs yet')).toBeInTheDocument()
    const portal = within(jobRowOf('Portal services'))
    expect(portal.getByText('2 services')).toBeInTheDocument()
    expect(portal.getByText('None')).toBeInTheDocument()
    expect(portal.getByText('Has problems (2)')).toBeInTheDocument()
    const billing = within(jobRowOf('Billing service only'))
    expect(billing.getByText('billing')).toBeInTheDocument()
  })

  it('says a job that was never read cannot run and shows where it lives', async () => {
    renderJobs()
    const link = await screen.findByRole('link', { name: 'release/yumdrake.yaml' })
    const row = within(link.closest('tr') as HTMLElement)
    expect(row.getByText('Has problems')).toBeInTheDocument()
    expect(row.getAllByText('Not available')).toHaveLength(2)
  })

  it('says "Not available" when the list does not carry services or approval', async () => {
    server.use(
      http.get('/api/v1/jobs', () =>
        HttpResponse.json({ jobs: mockState().jobs.map((job) => Object.fromEntries(Object.entries(job).filter(([key]) => !['definition', 'problems', 'problems_commit'].includes(key)))) }),
      ),
    )
    renderJobs()
    const row = within(await screen.findByRole('link', { name: 'Backend and microservices' }).then((a) => a.closest('tr') as HTMLElement))
    expect(row.getAllByText('Not available')).toHaveLength(2)
    expect(row.queryByText('None')).not.toBeInTheDocument()
  })

  it('narrows by title and by id when searching', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.type(screen.getByRole('searchbox', { name: 'Find a job' }), 'billing')
    expect(screen.getByRole('link', { name: 'Billing service only' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Portal services' })).not.toBeInTheDocument()
    expect(groupRows().map((row) => row.textContent)).toEqual([expect.stringMatching(/^Backend/)])
    await user.clear(screen.getByRole('searchbox', { name: 'Find a job' }))
    await user.type(screen.getByRole('searchbox', { name: 'Find a job' }), 'deploy-portal')
    expect(screen.getByRole('link', { name: 'Portal services' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Billing service only' })).not.toBeInTheDocument()
  })

  it('says no job matches a search and what to do', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.type(screen.getByRole('searchbox', { name: 'Find a job' }), 'zzz')
    expect(screen.getByText('No job matches your search')).toBeInTheDocument()
    expect(screen.getByText(/Clear the search/)).toBeInTheDocument()
  })

  it('shows one group when a folder is picked in the filter', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.selectOptions(screen.getByRole('combobox', { name: 'Folder' }), 'Portal')
    expect(screen.getByRole('link', { name: 'Portal services' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Billing service only' })).not.toBeInTheDocument()
    expect(groupRows()).toHaveLength(1)
    await user.selectOptions(screen.getByRole('combobox', { name: 'Folder' }), 'All folders')
    expect(screen.getByRole('link', { name: 'Billing service only' })).toBeInTheDocument()
  })

  it('says an empty folder has no jobs yet', async () => {
    const user = userEvent.setup()
    mockState().jobs = mockState().jobs.filter((job) => job.folder_id !== 3)
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    expect(screen.getByText('No jobs in this folder yet')).toBeInTheDocument()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Folder' }), 'Releases')
    expect(screen.getByText('No jobs in this folder yet')).toBeInTheDocument()
  })

  it('shows no buttons and no folder selects, and requests nothing it may not, without manage_jobs', async () => {
    renderJobs(viewerMe)
    await screen.findByRole('link', { name: 'Portal services' })
    expect(screen.queryByRole('button', { name: 'Add job' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New folder' })).not.toBeInTheDocument()
    expect(screen.queryByRole('combobox', { name: /Folder for/ })).not.toBeInTheDocument()
    expect(requests.filter((request) => request.method !== 'GET')).toEqual([])
    expect(requests.filter((request) => request.status === 403)).toEqual([])
  })

  it('shows the buttons and a folder select per job with manage_jobs', async () => {
    renderJobs(jobManagerMe)
    await screen.findByRole('link', { name: 'Portal services' })
    expect(screen.getByRole('button', { name: 'Add job' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New folder' })).toBeInTheDocument()
    expect(screen.getAllByRole('combobox', { name: /Folder for/ })).toHaveLength(4)
  })

  it('adds a job and opens its page', async () => {
    const user = userEvent.setup()
    const { location } = renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.click(screen.getByRole('button', { name: 'Add job' }))
    await user.selectOptions(screen.getByRole('combobox', { name: 'Folder for the new job' }), 'Portal')
    await user.type(screen.getByRole('textbox', { name: 'Repository' }), 'example-org/search')
    await user.clear(screen.getByRole('textbox', { name: 'Branch' }))
    await user.type(screen.getByRole('textbox', { name: 'Branch' }), 'main')
    await user.type(screen.getByRole('textbox', { name: 'Path to the job YAML' }), 'deploy/search.yaml')
    await user.click(screen.getByRole('button', { name: 'Read YAML and add job' }))
    expect(await screen.findByText('Job page opened')).toBeInTheDocument()
    const created = mockState().jobs.at(-1)
    expect(created).toMatchObject({ repo: 'example-org/search', branch: 'main', path: 'deploy/search.yaml', folder: 'Portal' })
    expect(location().pathname).toBe(`/jobs/${created?.id}`)
  })

  it('says a file that is already a job is registered, and keeps what was typed', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.click(screen.getByRole('button', { name: 'Add job' }))
    await user.type(screen.getByRole('textbox', { name: 'Repository' }), 'example-org/deploy-jobs')
    await user.type(screen.getByRole('textbox', { name: 'Path to the job YAML' }), 'jobs/deploy-backend.yaml')
    await user.click(screen.getByRole('button', { name: 'Read YAML and add job' }))
    expect(await screen.findByText('This file is already registered as a job')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Repository' })).toHaveValue('example-org/deploy-jobs')
  })

  it('shows a field error on its field', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.click(screen.getByRole('button', { name: 'Add job' }))
    await user.type(screen.getByRole('textbox', { name: 'Repository' }), 'not a repo')
    await user.type(screen.getByRole('textbox', { name: 'Path to the job YAML' }), 'jobs/x.yaml')
    await user.click(screen.getByRole('button', { name: 'Read YAML and add job' }))
    const field = screen.getByRole('textbox', { name: 'Repository' })
    await waitFor(() => expect(field).toBeInvalid())
    expect(field).toHaveAccessibleDescription(/must look like owner\/name/)
  })

  it('shows a refusal from the server next to the form', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.click(screen.getByRole('button', { name: 'Add job' }))
    await user.type(screen.getByRole('textbox', { name: 'Repository' }), 'example-org/search')
    await user.type(screen.getByRole('textbox', { name: 'Path to the job YAML' }), 'deploy/search.yaml')
    failNext('POST', '/api/v1/jobs', 403, 'forbidden', 'You do not have permission to do this')
    await user.click(screen.getByRole('button', { name: 'Read YAML and add job' }))
    expect(await screen.findByText('You do not have permission to do this')).toBeInTheDocument()
  })

  it('creates a folder, shows it at once and selects it in the filter', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.click(screen.getByRole('button', { name: 'New folder' }))
    await user.type(screen.getByRole('textbox', { name: 'Folder name' }), 'Search')
    await user.click(screen.getByRole('button', { name: 'Create folder' }))
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Folder' })).toHaveDisplayValue('Search'))
    expect(screen.getByText('No jobs in this folder yet')).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Folder name' })).not.toBeInTheDocument()
    expect(mockState().folders.map((folder) => folder.name)).toContain('Search')
  })

  it('shows a refused folder name next to the field', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    await user.click(screen.getByRole('button', { name: 'New folder' }))
    await user.type(screen.getByRole('textbox', { name: 'Folder name' }), 'Backend')
    await user.click(screen.getByRole('button', { name: 'Create folder' }))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Folder name' })).toHaveValue('Backend')
  })

  it('moves a job at once and keeps it there when the server agrees', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    delayNext('PATCH', '/api/v1/jobs/3', 200)
    await user.selectOptions(within(jobRowOf('Portal services')).getByRole('combobox', { name: /Folder for/ }), 'Releases')
    expect(groupRow('Releases')).toHaveTextContent('2 jobs')
    await waitFor(() => expect(mockState().jobs.find((job) => job.id === 3)?.folder).toBe('Releases'))
    expect(groupRow('Releases')).toHaveTextContent('2 jobs')
  })

  it('puts the job back with a message next to it when the server refuses the move', async () => {
    const user = userEvent.setup()
    renderJobs()
    await screen.findByRole('link', { name: 'Portal services' })
    failNext('PATCH', '/api/v1/jobs/3', 403, 'forbidden', 'You do not have permission to do this')
    await user.selectOptions(within(jobRowOf('Portal services')).getByRole('combobox', { name: /Folder for/ }), 'Releases')
    const row = within(jobRowOf('Portal services'))
    expect(await row.findByText(/could not move/i)).toBeInTheDocument()
    expect(row.getByText(/You do not have permission to do this/)).toBeInTheDocument()
    expect(row.getByRole('combobox', { name: /Folder for/ })).toHaveDisplayValue('Portal')
    expect(groupRow('Portal')).toHaveTextContent('1 job')
    expect(groupRow('Releases')).toHaveTextContent('1 job')
  })

  it('shows a loading state, then the jobs', async () => {
    delayNext('GET', '/api/v1/jobs', 100)
    renderJobs()
    expect(await screen.findByText('Loading')).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: 'Portal services' })).toBeInTheDocument()
  })

  it('shows a readable error with a retry when the list cannot be loaded', async () => {
    const user = userEvent.setup()
    failNext('GET', '/api/v1/jobs', 500, 'internal', 'The database is down')
    renderJobs()
    expect(await screen.findByRole('alert')).toHaveTextContent('The database is down')
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('link', { name: 'Portal services' })).toBeInTheDocument()
  })

  it('tells a person with nothing yet what to do', async () => {
    mockState().jobs = []
    mockState().folders = []
    renderJobs()
    expect(await screen.findByText('No jobs yet')).toBeInTheDocument()
    expect(screen.getByText(/Create a folder, then add a job/)).toBeInTheDocument()
  })
})
