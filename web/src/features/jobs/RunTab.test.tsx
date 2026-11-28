import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { Route, Routes } from 'react-router-dom'
import { adminMe, delayNext, failNext, mockState, qaMe, requests, server, viewerMe } from '../../test/server'
import { renderApp } from '../../test/render'
import { JobPage } from './JobPage'

function renderRunTab(jobId: number, search = '', me = adminMe) {
  return renderApp(
    <Routes>
      <Route path="/jobs/:id" element={<JobPage />} />
      <Route path="/runs/:id" element={<p>Run page</p>} />
    </Routes>,
    { route: `/jobs/${jobId}?tab=run${search}`, me },
  )
}

const planButton = () => screen.findByRole('button', { name: 'Plan this run' })
const environmentSelect = () => screen.findByRole('combobox', { name: 'Environment' })
const created = () => requests.filter((request) => request.method === 'POST' && /^\/api\/v1\/jobs\/\d+\/runs$/.test(request.path))

// Job 3 deploys backend-api and billing as one group, "Portal services". The mock's two services share no tag,
// so these tests arrange versions with a common tag.
function portalVersions() {
  const image = (tag: string) => ({ tag, digest: `sha256:${tag}`, pushed_at: '2026-11-26T12:00:00Z' })
  server.use(
    http.get('/api/v1/jobs/3/versions', ({ request }) => {
      const environment = new URL(request.url).searchParams.get('environment')
      const backend = ['1.9.4', '1.9.3', '1.9.2'].map(image)
      if (environment === 'hetzner-03') {
        return HttpResponse.json({
          groups: { portal: { common: ['1.9.4', '1.9.3', '1.9.2'], services: { 'backend-api': backend }, unavailable: { billing: 'not in this environment' } } },
        })
      }
      return HttpResponse.json({
        groups: { portal: { common: ['1.9.4', '1.9.3'], services: { 'backend-api': backend, billing: ['2.4.0', '1.9.4', '1.9.3'].map(image) }, unavailable: {} } },
      })
    }),
  )
}

const portalGroup = () => screen.findByRole('checkbox', { name: /Portal services/ })

describe('the Run tab', () => {
  it('lists only the environments whose label the job permits, each with its label', async () => {
    mockState().jobs.find((job) => job.id === 1)!.definition!.labels = ['dev', 'prod']
    renderRunTab(1)
    const select = await environmentSelect()
    const options = within(select).getAllByRole('option').map((option) => option.textContent)
    expect(options).toEqual(['hetzner-01 (dev)', 'hetzner-02 (dev)', 'hetzner-03 (dev)', 'prod-01 (prod)'])
  })

  it('keeps the environment choice in the URL beside the tab', async () => {
    const view = renderRunTab(3)
    await userEvent.selectOptions(await environmentSelect(), 'hetzner-02')
    expect(new URLSearchParams(view.location().search).get('env')).toBe('hetzner-02')
    expect(new URLSearchParams(view.location().search).get('tab')).toBe('run')
  })

  it('starts with every service selected and the newest common version chosen', async () => {
    portalVersions()
    renderRunTab(3)
    expect(await portalGroup()).toBeChecked()
    expect(screen.getByText('2 of 2 selected')).toBeInTheDocument()
    const version = screen.getByRole('combobox', { name: 'Portal services version' })
    expect(version).toHaveValue('1.9.4')
    const options = within(version).getAllByRole('option').map((option) => option.textContent)
    expect(options).toEqual(['1.9.4 (newest)', '1.9.3'])
  })

  it('counts the selected services of a group when one is unticked', async () => {
    portalVersions()
    renderRunTab(3)
    await portalGroup()
    await userEvent.click(screen.getByRole('button', { name: 'Choose services' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'billing' }))
    expect(screen.getByText('1 of 2 selected')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /Portal services/ })).not.toBeChecked()
    await userEvent.click(screen.getByRole('button', { name: 'Plan this run' }))
    await waitFor(() => expect(created()).toHaveLength(1))
    expect(created()[0]!.body).toEqual({
      environment: 'hetzner-01',
      selection: { portal: { version: '1.9.4', services: { 'backend-api': '1.9.4' } } },
      start: false,
    })
  })

  it('posts a version override for one service', async () => {
    portalVersions()
    renderRunTab(3)
    await portalGroup()
    await userEvent.click(screen.getByRole('button', { name: 'Choose services' }))
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Version for billing' }), '2.4.0')
    await userEvent.click(screen.getByRole('button', { name: 'Plan this run' }))
    await waitFor(() => expect(created()).toHaveLength(1))
    expect(created()[0]!.body).toEqual({
      environment: 'hetzner-01',
      selection: { portal: { version: '1.9.4', services: { 'backend-api': '1.9.4', billing: '2.4.0' } } },
      start: false,
    })
  })

  it('names every selected service even when nothing is overridden or unticked (the engine selects only named services), and goes to the new run', async () => {
    const view = renderRunTab(1)
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Backend services version' }), '1.9.2')
    await userEvent.click(await planButton())
    expect(await screen.findByText('Run page')).toBeInTheDocument()
    expect(created()[0]!.body).toEqual({ environment: 'hetzner-01', selection: { backends: { version: '1.9.2', services: { 'backend-api': '1.9.2' } } }, start: false })
    expect(view.location().pathname).toBe('/runs/110')
  })

  it('disables a service that does not exist in the environment and says why', async () => {
    portalVersions()
    renderRunTab(3, '&env=hetzner-03')
    await portalGroup()
    await userEvent.click(screen.getByRole('button', { name: 'Choose services' }))
    const billing = screen.getByRole('checkbox', { name: 'billing' })
    expect(billing).toBeDisabled()
    expect(billing).not.toBeChecked()
    expect(screen.getByText(/not in this environment/)).toBeInTheDocument()
    expect(screen.getByText('1 of 2 selected')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Plan this run' }))
    await waitFor(() => expect(created()).toHaveLength(1))
    expect(created()[0]!.body).toEqual({
      environment: 'hetzner-03',
      selection: { portal: { version: '1.9.4', services: { 'backend-api': '1.9.4' } } },
      start: false,
    })
  })

  it('disables the button while nothing is selected', async () => {
    portalVersions()
    renderRunTab(3)
    await userEvent.click(await portalGroup())
    expect(screen.getByText('0 of 2 selected')).toBeInTheDocument()
    expect(screen.getByText('Nothing is selected yet')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Plan this run' })).toBeDisabled()
    expect(created()).toHaveLength(0)
  })

  it('summarises the selection and warns when the label needs approval', async () => {
    renderRunTab(1, '&env=prod-01')
    expect(await screen.findByText(/This job needs approval in prod/)).toBeInTheDocument()
    expect(screen.getByText('1 service selected')).toBeInTheDocument()
    expect(screen.getByText('Backend services: 1 service at 1.9.4')).toBeInTheDocument()
  })

  it('shows the problems of a 422 next to the form and stays on the page', async () => {
    server.use(
      http.post('/api/v1/jobs/:id/runs', () =>
        HttpResponse.json(
          {
            error: {
              code: 'invalid',
              message: 'the selection cannot be planned',
              problems: [{ file: 'jobs/deploy-backend.yaml', line: 6, message: 'service "backend-api" has no config for hetzner-01' }],
            },
          },
          { status: 422 },
        ),
      ),
    )
    renderRunTab(1)
    await userEvent.click(await planButton())
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('service "backend-api" has no config for hetzner-01')).toBeInTheDocument()
    expect(within(alert).getByText('jobs/deploy-backend.yaml:6')).toBeInTheDocument()
    expect(screen.queryByText('Run page')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Plan this run' })).toBeEnabled()
  })

  it('shows another failure as a message and stays on the page', async () => {
    failNext('POST', '/api/v1/jobs/1/runs', 500, 'internal', 'The database is down')
    renderRunTab(1)
    await userEvent.click(await planButton())
    expect(await screen.findByText('The database is down')).toBeInTheDocument()
  })

  it('is read-only without the run permission for the label', async () => {
    renderRunTab(1, '', viewerMe)
    expect(await screen.findByText('Your role may not run jobs in dev')).toBeInTheDocument()
    expect(await screen.findByRole('checkbox', { name: /Backend services/ })).toBeDisabled()
    expect(screen.getByRole('combobox', { name: 'Backend services version' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Plan this run' })).not.toBeInTheDocument()
  })

  it('allows a label the person has a grant for and not another', async () => {
    renderRunTab(1, '&env=prod-01', qaMe)
    expect(await screen.findByText('Your role may not run jobs in prod')).toBeInTheDocument()
    await userEvent.selectOptions(await environmentSelect(), 'hetzner-01')
    await waitFor(() => expect(screen.queryByText(/Your role may not run jobs/)).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Plan this run' })).toBeEnabled()
  })

  it('says why a job that was never read cannot be run, instead of showing the form', async () => {
    renderRunTab(4)
    expect(await screen.findByText(/This job cannot be run/)).toBeInTheDocument()
    expect(screen.queryByRole('combobox', { name: 'Environment' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Plan this run' })).not.toBeInTheDocument()
  })

  it('shows the freeze banner for a frozen environment', async () => {
    renderRunTab(1, '&env=prod-01')
    expect(await screen.findByText(/Deployments to prod are frozen until .*: Quarter-end change freeze/)).toBeInTheDocument()
  })

  it('shows a loading state while the versions load', async () => {
    delayNext('GET', '/api/v1/jobs/1/versions', 200)
    renderRunTab(1)
    await environmentSelect()
    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(await planButton()).toBeInTheDocument()
  })

  it('shows an error with a retry when the versions cannot be read', async () => {
    failNext('GET', '/api/v1/jobs/1/versions', 502, 'upstream', 'ECR did not answer')
    renderRunTab(1)
    expect(await screen.findByText('ECR did not answer')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Plan this run' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await planButton()).toBeInTheDocument()
  })
})
