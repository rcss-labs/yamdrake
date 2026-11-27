import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import {
  accessManagerMe,
  connectionsManagerMe,
  delayNext,
  failNext,
  mockState,
  requests,
  server,
  viewerMe,
} from '../../test/server'

let queryClient: ReturnType<typeof renderApp>['queryClient']

async function openUsers(me = accessManagerMe) {
  const user = userEvent.setup()
  queryClient = renderApp(<App />, { route: '/admin/users', me }).queryClient
  await screen.findByRole('table', { name: 'Users' })
  return user
}

// TanStack keeps a mutation's variables in its state; the password must not be left there.
const mutationCacheText = () => JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state))

function rowOf(name: string) {
  return screen.getByRole('row', { name: new RegExp(name) })
}

const patches = () => requests.filter((r) => r.method === 'PATCH' && r.path.startsWith('/api/v1/admin/users/'))
const creates = () => requests.filter((r) => r.method === 'POST' && r.path === '/api/v1/admin/users')

async function fillCreateForm(
  user: ReturnType<typeof userEvent.setup>,
  values: { name: string; email: string; password: string; kind?: string },
) {
  if (values.name) {
    await user.type(screen.getByLabelText('Name'), values.name)
  }
  await user.type(screen.getByLabelText('Email'), values.email)
  await user.type(screen.getByLabelText('Password'), values.password)
  if (values.kind) {
    await user.selectOptions(screen.getByLabelText('Sign-in kind'), values.kind)
  }
}

describe('the users section', () => {
  it('lists each user with email, sign-in kind in words, groups, last sign-in and state', async () => {
    await openUsers()
    const dana = within(rowOf('Dana Levi'))
    expect(dana.getByText('dana@example.com')).toBeInTheDocument()
    expect(dana.getByText('Single sign-on')).toBeInTheDocument()
    expect(dana.getByText('Administrators')).toBeInTheDocument()
    expect(dana.getByText('Enabled')).toBeInTheDocument()
    const ci = within(rowOf('ci-pipeline'))
    expect(ci.getByText('Service')).toBeInTheDocument()
    expect(ci.getByText('Never')).toBeInTheDocument()
    expect(ci.getByText('QA')).toBeInTheDocument()
  })

  it('shows a loading state, then the table', async () => {
    delayNext('GET', '/api/v1/admin/users', 50)
    renderApp(<App />, { route: '/admin/users', me: accessManagerMe })
    expect(await screen.findByText('Loading')).toBeInTheDocument()
    expect(await screen.findByRole('table', { name: 'Users' })).toBeInTheDocument()
  })

  it('shows a readable error with a retry when the list cannot be loaded', async () => {
    const user = userEvent.setup()
    failNext('GET', '/api/v1/admin/users', 500, 'internal', 'The database is down')
    renderApp(<App />, { route: '/admin/users', me: accessManagerMe })
    expect(await screen.findByText('The database is down')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table', { name: 'Users' })).toBeInTheDocument()
  })

  it('says what to do when there are no users', async () => {
    server.use(http.get('/api/v1/admin/users', () => HttpResponse.json({ users: [] })))
    renderApp(<App />, { route: '/admin/users', me: accessManagerMe })
    expect(await screen.findByText('No users yet')).toBeInTheDocument()
    expect(screen.getByLabelText('Name')).toBeInTheDocument()
  })

  it('is absent without manage_access and nothing is requested from it', async () => {
    renderApp(<App />, { route: '/admin/users', me: connectionsManagerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Users' })).not.toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(requests.filter((r) => r.path.startsWith('/api/v1/admin/users'))).toEqual([])
  })

  it('shows a person with no grants the no-access page and requests nothing', async () => {
    renderApp(<App />, { route: '/admin/users', me: viewerMe })
    await waitFor(() => expect(screen.getByText('You do not have access')).toBeInTheDocument())
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole('tab', { name: 'Users' })).not.toBeInTheDocument()
    expect(requests.filter((r) => r.path.startsWith('/api/v1/admin/'))).toEqual([])
  })

  describe('creating a user', () => {
    it('uses a password field that is never pre-filled', async () => {
      await openUsers()
      const password = screen.getByLabelText('Password')
      expect(password).toHaveAttribute('type', 'password')
      expect(password).toHaveValue('')
    })

    it('posts the new user, shows it in the table and clears the password', async () => {
      const user = await openUsers()
      await fillCreateForm(user, { name: 'Robot Two', email: 'robot2@example.com', password: 'a-long-enough-pass', kind: 'service' })
      await user.click(screen.getByRole('button', { name: 'Create user' }))
      expect(await screen.findByRole('row', { name: /Robot Two/ })).toBeInTheDocument()
      expect(creates()[0]?.body).toEqual({
        name: 'Robot Two',
        email: 'robot2@example.com',
        source: 'service',
        password: 'a-long-enough-pass',
      })
      expect(screen.getByLabelText('Password')).toHaveValue('')
      expect(screen.getByLabelText('Name')).toHaveValue('')
    })

    it('keeps the password out of the mutation cache after a successful create', async () => {
      const user = await openUsers()
      await fillCreateForm(user, { name: 'Robot Two', email: 'robot2@example.com', password: 'a-long-enough-pass' })
      await user.click(screen.getByRole('button', { name: 'Create user' }))
      await screen.findByRole('row', { name: /Robot Two/ })
      await waitFor(() => expect(mutationCacheText()).not.toContain('a-long-enough-pass'))
    })

    it('keeps the password out of the mutation cache after a failed create, and still shows the error', async () => {
      const user = await openUsers()
      failNext('POST', '/api/v1/admin/users', 500, 'internal', 'The database is down')
      await fillCreateForm(user, { name: 'Robot Two', email: 'robot2@example.com', password: 'a-long-enough-pass' })
      await user.click(screen.getByRole('button', { name: 'Create user' }))
      expect(await screen.findByText('The database is down')).toBeInTheDocument()
      await waitFor(() => expect(mutationCacheText()).not.toContain('a-long-enough-pass'))
    })

    it('shows field errors on their fields and sends nothing', async () => {
      const user = await openUsers()
      await fillCreateForm(user, { name: '', email: 'not-an-email', password: 'short' })
      await user.click(screen.getByRole('button', { name: 'Create user' }))
      expect(screen.getByLabelText('Name')).toBeInvalid()
      expect(screen.getByLabelText('Email')).toBeInvalid()
      expect(screen.getByLabelText('Password')).toBeInvalid()
      expect(screen.getByText('Password must be at least 12 characters')).toBeInTheDocument()
      expect(creates()).toEqual([])
      expect(screen.getByLabelText('Password')).toHaveValue('')
    })

    it('shows a duplicate email on the email field and clears the password', async () => {
      const user = await openUsers()
      await fillCreateForm(user, { name: 'Dana Two', email: 'dana@example.com', password: 'a-long-enough-pass' })
      await user.click(screen.getByRole('button', { name: 'Create user' }))
      expect(await screen.findByText(/already exists/)).toBeInTheDocument()
      expect(screen.getByLabelText('Email')).toBeInvalid()
      expect(screen.getByLabelText('Password')).toHaveValue('')
      expect(screen.getByLabelText('Name')).toHaveValue('Dana Two')
    })

    it('shows a server failure next to the form and clears the password', async () => {
      const user = await openUsers()
      failNext('POST', '/api/v1/admin/users', 500, 'internal', 'The database is down')
      await fillCreateForm(user, { name: 'Robot Two', email: 'robot2@example.com', password: 'a-long-enough-pass' })
      await user.click(screen.getByRole('button', { name: 'Create user' }))
      expect(await screen.findByText('The database is down')).toBeInTheDocument()
      expect(screen.getByLabelText('Password')).toHaveValue('')
    })

    it('says plainly when the server refuses the person', async () => {
      const user = await openUsers()
      failNext('POST', '/api/v1/admin/users', 403, 'forbidden', 'You may not do this')
      await fillCreateForm(user, { name: 'Robot Two', email: 'robot2@example.com', password: 'a-long-enough-pass' })
      await user.click(screen.getByRole('button', { name: 'Create user' }))
      expect(await screen.findByText('You may not do this')).toBeInTheDocument()
    })
  })

  describe('disabling and enabling', () => {
    it('asks first, saying sessions and tokens stop, and does nothing on cancel', async () => {
      const user = await openUsers()
      await user.click(within(rowOf('Omer Katz')).getByRole('button', { name: 'Disable Omer Katz' }))
      expect(screen.getByText(/sessions and tokens stop working/)).toBeInTheDocument()
      expect(patches()).toEqual([])
      await user.click(screen.getByRole('button', { name: 'Cancel' }))
      expect(screen.queryByText(/sessions and tokens stop working/)).not.toBeInTheDocument()
      expect(patches()).toEqual([])
    })

    it('moves focus to the confirm button and back to the row button on cancel', async () => {
      const user = await openUsers()
      await user.click(screen.getByRole('button', { name: 'Disable Omer Katz' }))
      expect(screen.getByRole('button', { name: 'Confirm disable of Omer Katz' })).toHaveFocus()
      await user.click(screen.getByRole('button', { name: 'Cancel' }))
      expect(screen.getByRole('button', { name: 'Disable Omer Katz' })).toHaveFocus()
    })

    it('disables on confirm with a PATCH and shows the state, then enables without asking', async () => {
      const user = await openUsers()
      await user.click(screen.getByRole('button', { name: 'Disable Omer Katz' }))
      await user.click(screen.getByRole('button', { name: 'Confirm disable of Omer Katz' }))
      expect(await within(rowOf('Omer Katz')).findByText('Disabled')).toBeInTheDocument()
      expect(patches()[0]).toMatchObject({ path: '/api/v1/admin/users/3', body: { disabled: true } })

      await user.click(screen.getByRole('button', { name: 'Enable Omer Katz' }))
      expect(await within(rowOf('Omer Katz')).findByText('Enabled')).toBeInTheDocument()
      expect(patches()[1]?.body).toEqual({ disabled: false })
    })

    it('explains that at least one enabled admin must remain', async () => {
      const user = await openUsers()
      await user.click(screen.getByRole('button', { name: 'Disable Dana Levi' }))
      await user.click(screen.getByRole('button', { name: 'Confirm disable of Dana Levi' }))
      expect(await screen.findByText('At least one enabled admin must remain')).toBeInTheDocument()
      expect(within(rowOf('Dana Levi')).getByText('Enabled')).toBeInTheDocument()
    })
  })

  describe('editing group membership', () => {
    it('lists groups as checkboxes ticked for the current memberships', async () => {
      const user = await openUsers()
      await user.click(screen.getByRole('button', { name: 'Edit groups of Omer Katz' }))
      expect(await screen.findByRole('checkbox', { name: 'QA' })).toBeChecked()
      expect(screen.getByRole('checkbox', { name: 'Administrators' })).not.toBeChecked()
    })

    it('saves the chosen groups and updates the row', async () => {
      const user = await openUsers()
      await user.click(screen.getByRole('button', { name: 'Edit groups of Omer Katz' }))
      await user.click(await screen.findByRole('checkbox', { name: 'Administrators' }))
      await user.click(screen.getByRole('button', { name: 'Save groups' }))
      await waitFor(() => expect(within(rowOf('Omer Katz')).getByText('Administrators')).toBeInTheDocument())
      expect(patches()[0]).toMatchObject({ path: '/api/v1/admin/users/3', body: { group_ids: [1, 2] } })
      expect(screen.queryByRole('button', { name: 'Save groups' })).not.toBeInTheDocument()
    })

    it('shows a failed save next to the editor and keeps it open', async () => {
      const user = await openUsers()
      await user.click(screen.getByRole('button', { name: 'Edit groups of Omer Katz' }))
      await user.click(await screen.findByRole('checkbox', { name: 'Administrators' }))
      failNext('PATCH', '/api/v1/admin/users/3', 403, 'forbidden', 'You may not do this')
      await user.click(screen.getByRole('button', { name: 'Save groups' }))
      expect(await screen.findByText('You may not do this')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Save groups' })).toBeInTheDocument()
    })

    it('shows the last-admin message when removing the only administrator group member', async () => {
      const user = await openUsers()
      await user.click(screen.getByRole('button', { name: 'Edit groups of Dana Levi' }))
      await user.click(await screen.findByRole('checkbox', { name: 'Administrators' }))
      await user.click(screen.getByRole('button', { name: 'Save groups' }))
      expect(await screen.findByText('At least one enabled admin must remain')).toBeInTheDocument()
      expect(mockState().users.find((u) => u.id === 1)?.groups).toHaveLength(1)
    })
  })
})
