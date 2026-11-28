import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { JobDetail } from '../../api/types'
import { mockState, requests } from '../../test/server'
import { renderApp } from '../../test/render'
import { HistoryTab } from './HistoryTab'

const jobNumber = (id: number): JobDetail => {
  const job = mockState().jobs.find((candidate) => candidate.id === id)
  if (!job) throw new Error(`no job ${id}`)
  return job as JobDetail
}
const runLinks = () => screen.queryAllByRole('link', { name: /^#\d+$/ }).map((link) => link.textContent)
const lastSearch = () => new URLSearchParams(requests.filter((request) => request.path === '/api/v1/runs').at(-1)?.search ?? '')

describe('the History tab', () => {
  it("lists only this job's runs, without a Job column or a search box", async () => {
    renderApp(<HistoryTab job={jobNumber(3)} />, { route: '/jobs/3?tab=history' })
    await screen.findByRole('link', { name: '#106' })
    expect(runLinks()).toEqual(['#106', '#105', '#104', '#101'])
    expect(lastSearch().get('job')).toBe('3')
    expect(screen.queryByRole('columnheader', { name: 'Job' })).not.toBeInTheDocument()
    expect(screen.queryByRole('searchbox')).not.toBeInTheDocument()
  })

  it('filters by status and environment, keeping the tab in the URL', async () => {
    const view = renderApp(<HistoryTab job={jobNumber(3)} />, { route: '/jobs/3?tab=history' })
    await screen.findByRole('link', { name: '#106' })
    await userEvent.click(screen.getByRole('button', { name: 'Failed' }))
    await waitFor(() => expect(runLinks()).toEqual(['#106', '#105']))
    expect(lastSearch().getAll('state')).toEqual(['failed', 'partially_succeeded', 'needs_reconciliation'])
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Filter by environment' }), 'hetzner-02')
    await waitFor(() => expect(runLinks()).toEqual(['#105']))
    expect(lastSearch().get('environment')).toBe('hetzner-02')
    const params = new URLSearchParams(view.location().search)
    expect(params.get('tab')).toBe('history')
    expect(params.get('status')).toBe('failed')
    expect(params.get('environment')).toBe('hetzner-02')
  })

  it('says the job has not run yet when it has no runs at all', async () => {
    renderApp(<HistoryTab job={jobNumber(4)} />, { route: '/jobs/4?tab=history' })
    expect(await screen.findByText('This job has not run yet')).toBeInTheDocument()
  })

  it('says no run matches when filters hide every run', async () => {
    renderApp(<HistoryTab job={jobNumber(3)} />, { route: '/jobs/3?tab=history&status=succeeded' })
    expect(await screen.findByText('No runs match these filters')).toBeInTheDocument()
  })
})
