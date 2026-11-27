import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import type { AuditEntry } from '../../api/types'
import { renderApp } from '../../test/render'
import { auditorMe, delayNext, failNext, mockState, requests, viewerMe } from '../../test/server'

const auditRequests = () => requests.filter((request) => request.path === '/api/v1/admin/audit')

function manyEntries(count: number): AuditEntry[] {
  return Array.from({ length: count }, (_, index) => ({
    id: 100 + index,
    at: '2026-11-20T10:00:00Z',
    actor: `person${100 + index}@example.com`,
    action: 'folder.create',
    target_type: 'folder',
    target_id: String(100 + index),
    detail: {},
  }))
}

describe('the audit log section', () => {
  it('lists entries newest first with who, what and on what', async () => {
    renderApp(<App />, { route: '/admin/audit', me: auditorMe })
    const table = await screen.findByRole('table', { name: 'Audit log' })
    const headers = within(table).getAllByRole('columnheader').map((header) => header.textContent)
    expect(headers).toEqual(['When', 'Who', 'What', 'On what'])
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(5)
    expect(within(rows[0]!).getByText('omer@example.com')).toBeInTheDocument()
    expect(within(rows[0]!).getByText('auth.login')).toBeInTheDocument()
    expect(within(rows[0]!).getByText('user 3')).toHaveClass('mono')
    expect(within(rows[1]!).getByText('dana@example.com')).toBeInTheDocument()
  })

  it('says what to expect when the log is empty', async () => {
    mockState().audit = []
    renderApp(<App />, { route: '/admin/audit', me: auditorMe })
    expect(await screen.findByText('Nothing has been recorded yet')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('loads older entries with before=<last id> and keeps the earlier ones', async () => {
    mockState().audit = manyEntries(30)
    renderApp(<App />, { route: '/admin/audit', me: auditorMe })
    const table = await screen.findByRole('table', { name: 'Audit log' })
    const firstPage = within(table).getAllByRole('row').length - 1
    expect(firstPage).toBeLessThan(30)
    expect(auditRequests()[0]?.search).not.toContain('before=')
    const lastShownId = 129 - (firstPage - 1)

    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))

    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(31))
    expect(auditRequests()[1]?.search).toContain(`before=${lastShownId}`)
    expect(within(table).getByText('person129@example.com')).toBeInTheDocument()
    expect(within(table).getByText('person100@example.com')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('shows an error with retry when the log cannot be loaded', async () => {
    failNext('GET', '/api/v1/admin/audit', 500, 'internal', 'The audit log is unavailable.')
    renderApp(<App />, { route: '/admin/audit', me: auditorMe })
    expect(await screen.findByRole('alert')).toHaveTextContent('The audit log is unavailable.')
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table', { name: 'Audit log' })).toBeInTheDocument()
  })

  it('shows a loading state first', async () => {
    delayNext('GET', '/api/v1/admin/audit', 200)
    renderApp(<App />, { route: '/admin/audit', me: auditorMe })
    expect(await screen.findByText('Loading')).toBeInTheDocument()
    expect(await screen.findByRole('table', { name: 'Audit log' })).toBeInTheDocument()
  })

  it('keeps the shown entries and explains when loading older ones fails', async () => {
    mockState().audit = manyEntries(30)
    renderApp(<App />, { route: '/admin/audit', me: auditorMe })
    const table = await screen.findByRole('table', { name: 'Audit log' })
    const before = within(table).getAllByRole('row').length
    failNext('GET', '/api/v1/admin/audit', 500, 'internal', 'Older entries are unavailable.')
    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Older entries are unavailable.')
    expect(within(table).getAllByRole('row')).toHaveLength(before)
    expect(screen.getByRole('button', { name: 'Load older' })).toBeInTheDocument()
  })

  it('is absent without view_audit and nothing is requested for it', async () => {
    renderApp(<App />, { route: '/admin/audit', me: viewerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.queryByRole('table', { name: 'Audit log' })).not.toBeInTheDocument()
    expect(requests.filter((request) => request.path === '/api/v1/admin/audit')).toEqual([])
  })
})
