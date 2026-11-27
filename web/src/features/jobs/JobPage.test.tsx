import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router-dom'
import { delayNext, failNext, jobManagerMe, mockState, requests, viewerMe } from '../../test/server'
import { renderApp } from '../../test/render'
import { JobPage } from './JobPage'

function renderJob(route: string, me = jobManagerMe) {
  return renderApp(
    <Routes>
      <Route path="/jobs" element={<p>Jobs list</p>} />
      <Route path="/jobs/:id" element={<JobPage />} />
    </Routes>,
    { route, me },
  )
}

// Fixture job 1 has one group; make it a two-group job whose second step waits for the whole first step.
function makeTwoGroupJob() {
  const definition = mockState().jobs[0]!.definition!
  definition.groups = [
    { id: 'backends', label: 'Backend services', services: ['backend-api', 'worker'], line: 6 },
    { id: 'billing', label: 'Billing', services: ['billing'], line: 9 },
  ]
  definition.steps = [
    { name: 'config-backends', use: 'copy-config', group: 'backends', max_parallel: 4, timeout: 0, needs: null, line: 14 },
    { name: 'roll-backends', use: 'deploy-ecs', group: 'backends', max_parallel: 0, timeout: 1800 * 1e9, needs: [{ step: 'config-backends', scope: 'service', line: 19 }], line: 17 },
    { name: 'config-billing', use: 'copy-config', group: 'billing', max_parallel: 0, timeout: 0, needs: [{ step: 'config-backends', scope: 'step', line: 25 }], line: 22 },
  ]
}

const definitionPanel = () => screen.findByRole('tabpanel', { name: 'Definition' })

describe('the job page', () => {
  it('shows the header facts for a two-group job', async () => {
    makeTwoGroupJob()
    renderJob('/jobs/1')
    expect(await screen.findByRole('heading', { level: 1, name: 'Backend and microservices' })).toBeInTheDocument()
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'Jobs' })).toHaveAttribute('href', '/jobs')
    expect(within(crumbs).getByText('Backend')).toBeInTheDocument()
    expect(screen.getByText('deploy-backend')).toBeInTheDocument()
    expect(screen.getByText('Deploys 3 services')).toBeInTheDocument()
    expect(screen.getByText('Needs approval in prod')).toBeInTheDocument()
    const run = screen.getByRole('button', { name: 'Run this job' })
    expect(run).toBeDisabled()
    expect(screen.getByText('Running jobs arrives with the run screens')).toBeInTheDocument()
  })

  it('says no approval is needed when the definition has none', async () => {
    renderJob('/jobs/2')
    expect(await screen.findByText('No approval needed')).toBeInTheDocument()
  })

  it('shows not found for an unknown id, with a way back to Jobs', async () => {
    renderJob('/jobs/999')
    expect(await screen.findByRole('heading', { name: 'Job not found' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('link', { name: 'Go to Jobs' }))
    expect(await screen.findByText('Jobs list')).toBeInTheDocument()
  })

  it('shows a loading state, then a readable error with a retry', async () => {
    delayNext('GET', '/api/v1/jobs/1', 50)
    failNext('GET', '/api/v1/jobs/1', 500, 'internal', 'The database is down')
    renderJob('/jobs/1')
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(await screen.findByText('The database is down')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Backend and microservices' })).toBeInTheDocument()
  })

  it('lists groups, services and steps with the dependency wording for both scopes', async () => {
    makeTwoGroupJob()
    renderJob('/jobs/1')
    const panel = await definitionPanel()
    expect(within(panel).getByText('Backend services')).toBeInTheDocument()
    expect(within(panel).getByText('backend-api')).toBeInTheDocument()
    expect(within(panel).getByText('worker')).toBeInTheDocument()
    expect(within(panel).getByText('Billing')).toBeInTheDocument()
    const roll = within(panel).getByRole('listitem', { name: 'roll-backends' })
    expect(within(roll).getByText('deploy-ecs')).toBeInTheDocument()
    expect(within(roll).getByText('After config-backends, per service')).toBeInTheDocument()
    expect(within(roll).getByText('Timeout 30 minutes')).toBeInTheDocument()
    const billing = within(panel).getByRole('listitem', { name: 'config-billing' })
    expect(within(billing).getByText('After config-backends, the whole step')).toBeInTheDocument()
    const first = within(panel).getByRole('listitem', { name: 'config-backends' })
    expect(within(first).getByText('Up to 4 at a time')).toBeInTheDocument()
  })

  it('shows where the job is read from and when', async () => {
    renderJob('/jobs/1')
    const panel = await definitionPanel()
    expect(within(panel).getByText('example-org/deploy-jobs')).toBeInTheDocument()
    expect(within(panel).getByText('main')).toBeInTheDocument()
    expect(within(panel).getByText('jobs/deploy-backend.yaml')).toBeInTheDocument()
    expect(within(panel).getByText('1111111')).toBeInTheDocument()
  })

  it('warns that the last good version is in use and lists each problem as file:line', async () => {
    renderJob('/jobs/3')
    const panel = await definitionPanel()
    const note = within(panel).getByText(/last good version/i).closest('.ui-note') as HTMLElement
    expect(within(note).getByText(/is still in use/i)).toBeInTheDocument()
    expect(within(note).getByText('3333333')).toBeInTheDocument()
    expect(within(note).getByText('4444444')).toBeInTheDocument()
    expect(within(panel).getByText('deploy/yumdrake.yaml:17')).toBeInTheDocument()
    expect(within(panel).getByText('deploy/yumdrake.yaml:23')).toBeInTheDocument()
  })

  it('shows the reason a never-read job cannot run, without crashing', async () => {
    renderJob('/jobs/4')
    expect(await screen.findByRole('heading', { level: 1, name: 'release/yumdrake.yaml' })).toBeInTheDocument()
    const panel = await definitionPanel()
    expect(within(panel).getByRole('alert')).toHaveTextContent('was not found or the connection has no access')
    expect(screen.queryByText('No approval needed')).not.toBeInTheDocument()
  })

  it('shows no file block when the response carries no source', async () => {
    renderJob('/jobs/1')
    await definitionPanel()
    expect(screen.queryByRole('region', { name: 'Job file' })).not.toBeInTheDocument()
  })

  it('shows the raw YAML when the response carries it', async () => {
    ;(mockState().jobs[0] as unknown as Record<string, unknown>).source = 'id: deploy-backend\n'
    renderJob('/jobs/1')
    expect(await screen.findByRole('region', { name: 'Job file' })).toHaveTextContent('id: deploy-backend')
  })

  it('re-reads from GitHub and refreshes the job', async () => {
    renderJob('/jobs/1')
    const button = await screen.findByRole('button', { name: 'Re-read from GitHub' })
    await userEvent.click(button)
    await waitFor(() => expect(requests.some((r) => r.method === 'POST' && r.path === '/api/v1/jobs/1/sync')).toBe(true))
    await waitFor(() => expect(requests.filter((r) => r.method === 'GET' && r.path === '/api/v1/jobs/1').length).toBeGreaterThan(1))
    expect(await screen.findByRole('button', { name: 'Re-read from GitHub' })).toBeEnabled()
  })

  it('shows a sync failure next to the button', async () => {
    failNext('POST', '/api/v1/jobs/1/sync', 502, 'upstream', 'GitHub did not answer')
    renderJob('/jobs/1')
    await userEvent.click(await screen.findByRole('button', { name: 'Re-read from GitHub' }))
    expect(await screen.findByText('GitHub did not answer')).toBeInTheDocument()
  })

  it('hides Re-read from GitHub without manage_jobs and asks the server for nothing it would refuse', async () => {
    renderJob('/jobs/1', viewerMe)
    await definitionPanel()
    expect(screen.queryByRole('button', { name: 'Re-read from GitHub' })).not.toBeInTheDocument()
    expect(requests.some((r) => r.method === 'POST')).toBe(false)
  })

  it('keeps the tab in the URL', async () => {
    const view = renderJob('/jobs/1')
    await definitionPanel()
    await userEvent.click(screen.getByRole('tab', { name: 'History' }))
    expect(view.location().search).toBe('?tab=history')
    expect(screen.getByRole('tabpanel', { name: 'History' })).toHaveTextContent('run screens')
    await userEvent.click(screen.getByRole('tab', { name: 'Run' }))
    expect(view.location().search).toBe('?tab=run')
  })

  it('opens the tab named in the URL', async () => {
    renderJob('/jobs/1?tab=run')
    expect(await screen.findByRole('tabpanel', { name: 'Run' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Run' })).toHaveAttribute('aria-selected', 'true')
  })
})
