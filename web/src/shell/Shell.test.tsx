import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../App'
import { adminMe, failNext, jobManagerMe, requests, viewerMe } from '../test/server'
import { renderApp } from '../test/render'

async function mainNav() {
  return within(await screen.findByRole('navigation', { name: 'Main' }))
}

describe('the application shell', () => {
  it('lists eight sections for an administrator', async () => {
    renderApp(<App />, { me: adminMe })
    const nav = await mainNav()
    expect(nav.getAllByRole('link').map((link) => link.textContent)).toEqual([
      'Overview',
      'Jobs',
      'Runs',
      'Approvals',
      'Versions',
      'Inventory',
      'CLI',
      'Admin',
    ])
  })

  it('lists seven sections, without Admin, for a person with no grants', async () => {
    renderApp(<App />, { me: viewerMe })
    const nav = await mainNav()
    expect(nav.getAllByRole('link')).toHaveLength(7)
    expect(nav.queryByRole('link', { name: 'Admin' })).not.toBeInTheDocument()
  })

  it('shows Admin to a person who manages jobs', async () => {
    renderApp(<App />, { me: jobManagerMe })
    expect((await mainNav()).getByRole('link', { name: 'Admin' })).toBeInTheDocument()
  })

  it('marks the current section and follows the route', async () => {
    renderApp(<App />, { route: '/jobs/3' })
    const nav = await mainNav()
    expect(nav.getByRole('link', { name: 'Jobs' })).toHaveAttribute('aria-current', 'page')
    expect(nav.getByRole('link', { name: 'Overview' })).not.toHaveAttribute('aria-current')
    await userEvent.click(nav.getByRole('link', { name: 'Inventory' }))
    expect(nav.getByRole('link', { name: 'Inventory' })).toHaveAttribute('aria-current', 'page')
    expect(nav.getByRole('link', { name: 'Jobs' })).not.toHaveAttribute('aria-current')
  })

  it('marks Overview only on the home page', async () => {
    renderApp(<App />, { route: '/' })
    const nav = await mainNav()
    expect(nav.getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument()
  })

  it('has one main landmark and a labelled navigation', async () => {
    renderApp(<App />)
    await mainNav()
    expect(screen.getAllByRole('main')).toHaveLength(1)
    expect(screen.getAllByRole('navigation', { name: 'Main' })).toHaveLength(1)
  })

  it('shows who is signed in and signs out', async () => {
    renderApp(<App />, { me: viewerMe })
    expect(await screen.findByText(viewerMe.name)).toBeInTheDocument()
    expect(screen.getByText(viewerMe.email)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument()
  })

  it('says so in plain words when sign-out could not reach the server', async () => {
    renderApp(<App />)
    const button = await screen.findByRole('button', { name: 'Sign out' })
    failNext('POST', '/api/v1/auth/logout', 500, 'internal', 'The server had a problem.')
    await userEvent.click(button)
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not sign you out/i)
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
  })

  it('shows a page not found message with a link to Jobs for an unknown path', async () => {
    renderApp(<App />, { route: '/nowhere' })
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument()
    const main = within(screen.getByRole('main'))
    expect(main.getByRole('link', { name: 'Go to Jobs' })).toHaveAttribute('href', '/jobs')
  })

  it('does not flash page not found when a signed-in person opens the sign-in address', async () => {
    const { location } = renderApp(<App />, { route: '/sign-in' })
    expect(await screen.findByRole('navigation', { name: 'Main' })).toBeInTheDocument()
    expect(screen.queryByText('Page not found')).not.toBeInTheDocument()
    expect(location().pathname).toBe('/')
  })

  it('shows the placeholder pages that have no screen yet with their title', async () => {
    for (const [route, title] of [
      ['/runs', 'Runs'],
      ['/approvals', 'Approvals'],
      ['/versions', 'Versions'],
    ] as const) {
      const { unmount } = renderApp(<App />, { route })
      expect(await screen.findByRole('heading', { level: 1, name: title })).toBeInTheDocument()
      unmount()
    }
  })

  it('shows no access, and asks the admin endpoints for nothing, to a non-admin on an admin address', async () => {
    renderApp(<App />, { route: '/admin/users', me: viewerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    expect(requests.filter((request) => request.path.startsWith('/api/v1/admin/'))).toEqual([])
  })
})
