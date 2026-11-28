import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { adminMe, eventSources, qaMe, requests, runIds, viewerMe } from '../../test/server'
import { actionNames, moveStoredRun, renderRun, storedRun } from './runTestUtils'

const lead = () => screen.findByTestId('run-lead')

describe('the run page header', () => {
  it('shows the crumbs, the title with its status and the facts row', async () => {
    renderRun(runIds.partiallySucceeded)
    expect(await screen.findByRole('heading', { level: 1, name: /Run #105/ })).toHaveTextContent('Partly succeeded')
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'Jobs' })).toHaveAttribute('href', '/jobs')
    const facts = screen.getByText('Environment').closest('dl') as HTMLElement
    expect(within(facts).getByText('hetzner-02')).toBeInTheDocument()
    expect(within(facts).getByText(/Dana Levi, web/)).toBeInTheDocument()
    expect(within(facts).getByText('7m 30s')).toBeInTheDocument()
    expect(within(facts).getByRole('img', { name: /services/ })).toBeInTheDocument()
  })

  it('says the run cannot be found for an unknown id', async () => {
    renderRun(999)
    expect(await screen.findByText(/Run not found|not found/i)).toBeInTheDocument()
  })
})

describe('the sentence that says where the run stands', () => {
  it('planned', async () => {
    renderRun(runIds.planned)
    expect(await lead()).toHaveTextContent('Review the plan, then start')
  })

  it('resolving', async () => {
    moveStoredRun(runIds.planned, 'resolving')
    renderRun(runIds.planned)
    expect(await lead()).toHaveTextContent('Planning: reading current versions and config')
  })

  it('waiting for approval, with the expiry', async () => {
    renderRun(runIds.waitingApproval)
    expect(await lead()).toHaveTextContent(/Waiting for approval, expires/)
  })

  it('queued behind another run, with a link to it', async () => {
    moveStoredRun(runIds.running, 'queued', { wait_reason: 'Waiting for run #105 to finish' })
    renderRun(runIds.running)
    const link = await within(await lead()).findByRole('link', { name: 'run #105' })
    expect(link).toHaveAttribute('href', '/runs/105')
    expect(await lead()).toHaveTextContent('Waiting for run #105 to finish')
  })

  it('queued behind a freeze, in the server words', async () => {
    moveStoredRun(runIds.running, 'queued', { wait_reason: 'Frozen: Quarter-end change freeze, until 30 Nov' })
    renderRun(runIds.running)
    expect(await lead()).toHaveTextContent('Frozen: Quarter-end change freeze, until 30 Nov')
  })

  it('running counts services updated so far', async () => {
    renderRun(runIds.running)
    expect(await lead()).toHaveTextContent('0 of 2 services updated so far')
  })

  it('cancelling', async () => {
    moveStoredRun(runIds.running, 'cancelling', { cancel_requested: true })
    renderRun(runIds.running)
    expect(await lead()).toHaveTextContent('Cancelling: waiting for services already rolling to finish')
  })

  it('needs reconciliation counts the unconfirmed outcomes', async () => {
    renderRun(runIds.needsReconciliation)
    expect(await lead()).toHaveTextContent('2 outcomes could not be confirmed. Its services stay locked until they are resolved')
  })

  it('succeeded', async () => {
    renderRun(runIds.succeeded)
    expect(await lead()).toHaveTextContent(/^All \d+ services? updated$/)
  })

  it('partially succeeded', async () => {
    renderRun(runIds.partiallySucceeded)
    expect(await lead()).toHaveTextContent('1 of 2 services updated')
  })

  it('cancelled says by whom and how much was already updated', async () => {
    moveStoredRun(runIds.partiallySucceeded, 'cancelled', { end_reason: 'Cancelled by Dana Levi' })
    renderRun(runIds.partiallySucceeded)
    expect(await lead()).toHaveTextContent('Cancelled by Dana Levi. 1 service had already been updated')
  })

  it('invalidated gives the detail', async () => {
    renderRun(runIds.invalidated)
    expect(await lead()).toHaveTextContent('The targets changed after this plan was made. Billing changed after the plan was made')
  })

  it('rejected says who and when', async () => {
    moveStoredRun(runIds.waitingApproval, 'rejected', { end_reason: 'Rejected by Dana Levi', state_changed_at: '2026-11-27T11:55:00Z' })
    renderRun(runIds.waitingApproval)
    expect(await lead()).toHaveTextContent(/Rejected by Dana Levi/)
  })
})

describe('what failed', () => {
  it('lists the failed item, its step and reason, and a line for the skipped steps', async () => {
    renderRun(runIds.partiallySucceeded)
    const panel = await screen.findByRole('region', { name: 'What failed' })
    const rows = within(panel).getAllByRole('row')
    expect(rows).toHaveLength(2)
    expect(within(rows[1]!).getByText('billing')).toBeInTheDocument()
    expect(within(rows[1]!).getByText('copy-config')).toBeInTheDocument()
    expect(within(rows[1]!).getByText(/does not exist \(NoSuchKey\)/)).toBeInTheDocument()
    expect(within(panel).getByText(/billing: deploy and verify were skipped/)).toBeInTheDocument()
  })

  it('"Show its log" puts the service in the address, which is the console scope', async () => {
    const view = renderRun(runIds.partiallySucceeded)
    await userEvent.click(await screen.findByRole('button', { name: 'Show its log' }))
    expect(view.location().search).toBe('?service=billing')
  })

  it('is not shown for a run that succeeded', async () => {
    renderRun(runIds.succeeded)
    await lead()
    expect(screen.queryByRole('region', { name: 'What failed' })).not.toBeInTheDocument()
  })
})

describe('watching a run change under the page', () => {
  it('swaps the buttons when a state event moves 103 from waiting approval to running', async () => {
    renderRun(runIds.waitingApproval)
    expect(await actionNames()).toEqual(['Approve', 'Reject', 'Cancel'])
    const run = storedRun(runIds.waitingApproval)
    await waitFor(() => expect(eventSources.all.length).toBeGreaterThan(0))
    act(() => {
      eventSources.last()?.open()
      eventSources.last()?.emit('state', {
        id: run.id,
        state: 'running',
        wait_reason: '',
        end_reason: '',
        state_changed_at: '2026-11-27T12:01:00Z',
        started_at: '2026-11-27T12:01:00Z',
        finished_at: null,
        cancel_requested: false,
        legs: run.legs,
        locks: run.locks ?? [],
        summary: run.summary,
      })
    })
    await waitFor(async () => expect(await actionNames()).toEqual(['Cancel']))
  })
})

describe('who can see logs', () => {
  it('makes no log request for a person who may not view logs', async () => {
    renderRun(runIds.succeeded, viewerMe)
    await lead()
    await waitFor(() => expect(requests.some((request) => request.path === `/api/v1/runs/${runIds.succeeded}`)).toBe(true))
    expect(requests.filter((request) => request.path.endsWith('/log'))).toEqual([])
  })

  it('reads the log for an admin', async () => {
    renderRun(runIds.succeeded, adminMe)
    await lead()
    await waitFor(() => expect(requests.some((request) => request.path.endsWith('/log'))).toBe(true))
  })

  it('does not read a prod run log for a person with logs in dev only', async () => {
    renderRun(runIds.waitingApproval, qaMe)
    await lead()
    expect(requests.filter((request) => request.path.endsWith('/log'))).toEqual([])
  })
})
