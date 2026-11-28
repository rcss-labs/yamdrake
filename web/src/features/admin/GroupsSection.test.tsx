import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { accessManagerMe, delayNext, failNext, mockState, requests, viewerMe } from '../../test/server'

const GLOBAL_ACTIONS = [
  ['Manage jobs', 'manage_jobs'],
  ['Manage connections', 'manage_connections'],
  ['Manage access', 'manage_access'],
  ['View audit log', 'view_audit'],
] as const

async function openGroups(me = accessManagerMe) {
  const rendered = renderApp(<App />, { route: '/admin/groups', me })
  await screen.findByRole('region', { name: 'QA' })
  return rendered
}

const group = (name: string) => screen.getByRole('region', { name })

async function startGrant(user: ReturnType<typeof userEvent.setup>, groupName: string, actionLabel: string) {
  const form = within(group(groupName)).getByRole('form', { name: `Add grant to ${groupName}` })
  await user.selectOptions(within(form).getByLabelText('Action'), actionLabel)
  return form
}

describe('the groups and permissions section', () => {
  it('lists each group with its member count, admin switch and grants in words', async () => {
    await openGroups()
    const qa = within(group('QA'))
    expect(qa.getByRole('switch', { name: 'Administrator group: QA' })).not.toBeChecked()
    expect(group('QA')).toHaveTextContent(/\d+ members?/)
    const grants = qa.getByRole('table', { name: 'Grants of QA' })
    expect(within(grants).getByText('Run jobs')).toBeInTheDocument()
    expect(within(grants).getByText('View logs')).toBeInTheDocument()
    const approve = within(grants).getByRole('row', { name: /Approve runs/ })
    expect(approve).toHaveTextContent('Label preprod')
    expect(approve).toHaveTextContent('Portal')
    expect(within(group('Administrators')).getByRole('switch', { name: 'Administrator group: Administrators' })).toBeChecked()
  })

  it('shows an empty state for a group without grants and for no mappings', async () => {
    mockState().mappings = []
    mockState().grants = []
    await openGroups()
    expect(within(group('QA')).getByText('No grants yet')).toBeInTheDocument()
    expect(within(group('QA')).getByText(/A group with no grants can only view/)).toBeInTheDocument()
    expect(screen.getByText('No identity provider groups are mapped')).toBeInTheDocument()
  })

  it('tells that an administrator group needs no grants', async () => {
    await openGroups()
    const admins = within(group('Administrators'))
    expect(admins.getByText('Administrators can do everything. Grants are not needed for this group.')).toBeInTheDocument()
    expect(admins.queryByText(/can only view/)).not.toBeInTheDocument()
  })

  it('shows an empty state when there are no groups', async () => {
    mockState().groups = []
    mockState().grants = []
    renderApp(<App />, { route: '/admin/groups', me: accessManagerMe })
    expect(await screen.findByText('No groups yet')).toBeInTheDocument()
    expect(screen.getByLabelText('Group name')).toBeInTheDocument()
  })

  it('creates a group and shows it', async () => {
    const user = userEvent.setup()
    await openGroups()
    await user.type(screen.getByLabelText('Group name'), 'Release managers')
    await user.click(screen.getByRole('button', { name: 'Create group' }))
    expect(await screen.findByRole('region', { name: 'Release managers' })).toBeInTheDocument()
    expect(requests.find((r) => r.method === 'POST' && r.path === '/api/v1/admin/groups')?.body).toEqual({ name: 'Release managers' })
    expect(screen.getByLabelText('Group name')).toHaveValue('')
  })

  it('shows the server error next to the create form', async () => {
    const user = userEvent.setup()
    await openGroups()
    await user.type(screen.getByLabelText('Group name'), 'QA')
    await user.click(screen.getByRole('button', { name: 'Create group' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/already exists/)
  })

  it('makes a group an admin group with PATCH admin', async () => {
    const user = userEvent.setup()
    await openGroups()
    await user.click(within(group('QA')).getByRole('switch', { name: 'Administrator group: QA' }))
    await waitFor(() => expect(within(group('QA')).getByRole('switch', { name: 'Administrator group: QA' })).toBeChecked())
    const patch = requests.find((r) => r.method === 'PATCH' && r.path === '/api/v1/admin/groups/2')
    expect(patch?.body).toEqual({ admin: true })
  })

  it.each([
    ['makes a group an admin group', async (user: ReturnType<typeof userEvent.setup>) => {
      await user.click(within(group('QA')).getByRole('switch', { name: 'Administrator group: QA' }))
    }],
    ['creates a group', async (user: ReturnType<typeof userEvent.setup>) => {
      await user.type(screen.getByLabelText('Group name'), 'Release managers')
      await user.click(screen.getByRole('button', { name: 'Create group' }))
    }],
    ['removes a grant', async (user: ReturnType<typeof userEvent.setup>) => {
      const row = within(group('QA')).getByRole('row', { name: /View logs/ })
      await user.click(within(row).getByRole('button', { name: 'Remove grant' }))
      await user.click(within(row).getByRole('button', { name: 'Confirm remove' }))
    }],
    ['removes a mapping', async (user: ReturnType<typeof userEvent.setup>) => {
      const row = screen.getByRole('row', { name: /eng-qa/ })
      await user.click(within(row).getByRole('button', { name: 'Remove mapping' }))
      await user.click(within(row).getByRole('button', { name: 'Confirm remove' }))
    }],
  ])('asks for the updated permissions after the person %s', async (_name, act) => {
    const user = userEvent.setup()
    await openGroups()
    const meRequests = () => requests.filter((r) => r.method === 'GET' && r.path === '/api/v1/me').length
    const before = meRequests()
    await act(user)
    await waitFor(() => expect(meRequests()).toBe(before + 1))
  })

  it('says at least one admin must remain when the last admin group is turned off', async () => {
    const user = userEvent.setup()
    await openGroups()
    await user.click(within(group('Administrators')).getByRole('switch', { name: 'Administrator group: Administrators' }))
    expect(await within(group('Administrators')).findByRole('alert')).toHaveTextContent('At least one enabled admin must remain')
    expect(within(group('Administrators')).getByRole('switch', { name: 'Administrator group: Administrators' })).toBeChecked()
  })

  it('shows a 403 on the admin switch in place', async () => {
    const user = userEvent.setup()
    await openGroups()
    failNext('PATCH', '/api/v1/admin/groups/2', 403, 'forbidden', 'You may not change groups')
    await user.click(within(group('QA')).getByRole('switch', { name: 'Administrator group: QA' }))
    expect(await within(group('QA')).findByRole('alert')).toHaveTextContent('You may not change groups')
  })

  it('adds a scoped grant with label, environment and folder choices from the inventory and folders', async () => {
    const user = userEvent.setup()
    await openGroups()
    const form = await startGrant(user, 'QA', 'Restore versions')
    expect(within(within(form).getByLabelText('Label')).getAllByRole('option').map((o) => o.textContent)).toEqual([
      'Any label',
      'dev',
      'preprod',
      'prod',
    ])
    expect(within(within(form).getByLabelText('Environment')).getByRole('option', { name: 'hetzner-01' })).toBeInTheDocument()
    expect(within(within(form).getByLabelText('Folder')).getByRole('option', { name: 'Releases' })).toBeInTheDocument()
    await user.selectOptions(within(form).getByLabelText('Label'), 'prod')
    await user.selectOptions(within(form).getByLabelText('Folder'), 'Releases')
    await user.click(within(form).getByRole('button', { name: 'Add grant' }))
    const row = await within(group('QA')).findByRole('row', { name: /Restore versions/ })
    expect(row).toHaveTextContent('Label prod')
    expect(row).toHaveTextContent('Releases')
    const post = requests.find((r) => r.method === 'POST' && r.path === '/api/v1/admin/grants')
    expect(post?.body).toEqual({ group_id: 2, action: 'restore', label: 'prod', folder_id: 3 })
  })

  it('adds an environment-scoped grant', async () => {
    const user = userEvent.setup()
    await openGroups()
    const form = await startGrant(user, 'QA', 'Cancel runs')
    await user.selectOptions(within(form).getByLabelText('Environment'), 'hetzner-02')
    await user.click(within(form).getByRole('button', { name: 'Add grant' }))
    expect(await within(group('QA')).findByRole('row', { name: /Cancel runs/ })).toHaveTextContent('Environment hetzner-02')
    const post = requests.find((r) => r.method === 'POST' && r.path === '/api/v1/admin/grants')
    expect(post?.body).toEqual({ group_id: 2, action: 'cancel', environment: 'hetzner-02' })
  })

  it('lets a grant carry a label or an environment, never both', async () => {
    const user = userEvent.setup()
    await openGroups()
    const form = await startGrant(user, 'QA', 'Run jobs')
    const label = within(form).getByLabelText('Label')
    const environment = within(form).getByLabelText('Environment')

    await user.selectOptions(environment, 'hetzner-02')
    expect(label).toBeDisabled()
    expect(label).toHaveValue('')
    await user.selectOptions(environment, 'Any environment')
    expect(label).toBeEnabled()

    await user.selectOptions(label, 'dev')
    expect(environment).toBeDisabled()
    await user.click(within(form).getByRole('button', { name: 'Add grant' }))
    await waitFor(() => expect(requests.some((r) => r.method === 'POST' && r.path === '/api/v1/admin/grants')).toBe(true))
    expect(requests.find((r) => r.method === 'POST' && r.path === '/api/v1/admin/grants')?.body).toEqual({
      group_id: 2,
      action: 'run',
      label: 'dev',
    })
  })

  it.each(GLOBAL_ACTIONS)('never offers a scope for the global action %s', async (label, action) => {
    const user = userEvent.setup()
    await openGroups()
    // Pick a scoped action first so a leftover scope would be visible.
    const form = await startGrant(user, 'QA', 'Run jobs')
    await user.selectOptions(within(form).getByLabelText('Label'), 'dev')
    await user.selectOptions(within(form).getByLabelText('Action'), label)
    expect(within(form).queryByLabelText('Label')).not.toBeInTheDocument()
    expect(within(form).queryByLabelText('Environment')).not.toBeInTheDocument()
    expect(within(form).queryByLabelText('Folder')).not.toBeInTheDocument()
    await user.click(within(form).getByRole('button', { name: 'Add grant' }))
    await waitFor(() => expect(requests.some((r) => r.method === 'POST' && r.path === '/api/v1/admin/grants')).toBe(true))
    const post = requests.filter((r) => r.method === 'POST' && r.path === '/api/v1/admin/grants').at(-1)
    expect(post?.body).toEqual({ group_id: 2, action })
  })

  it('shows a grant error next to the add form', async () => {
    const user = userEvent.setup()
    await openGroups()
    const form = await startGrant(user, 'QA', 'Run jobs')
    failNext('POST', '/api/v1/admin/grants', 403, 'forbidden', 'You may not add grants')
    await user.click(within(form).getByRole('button', { name: 'Add grant' }))
    expect(await within(form).findByRole('alert')).toHaveTextContent('You may not add grants')
  })

  it('asks for confirmation before removing a grant', async () => {
    const user = userEvent.setup()
    await openGroups()
    const row = within(group('QA')).getByRole('row', { name: /View logs/ })
    await user.click(within(row).getByRole('button', { name: 'Remove grant' }))
    expect(requests.some((r) => r.method === 'DELETE')).toBe(false)
    await user.click(within(row).getByRole('button', { name: 'Keep grant' }))
    expect(requests.some((r) => r.method === 'DELETE')).toBe(false)
    await user.click(within(row).getByRole('button', { name: 'Remove grant' }))
    await user.click(within(row).getByRole('button', { name: 'Confirm remove' }))
    await waitFor(() => expect(within(group('QA')).queryByRole('row', { name: /View logs/ })).not.toBeInTheDocument())
    expect(requests.some((r) => r.method === 'DELETE' && r.path === '/api/v1/admin/grants/2')).toBe(true)
  })

  it('lists identity provider group mappings', async () => {
    await openGroups()
    const table = screen.getByRole('table', { name: 'Identity provider group mappings' })
    expect(within(table).getByText('eng-devops')).toBeInTheDocument()
    expect(within(within(table).getByRole('row', { name: /eng-qa/ })).getByLabelText('Yumdrake group for eng-qa')).toHaveValue('2')
  })

  it('adds, changes and removes a mapping', async () => {
    const user = userEvent.setup()
    await openGroups()
    await user.type(screen.getByLabelText('Identity provider group'), 'eng/ops team')
    await user.selectOptions(screen.getByLabelText('Yumdrake group'), 'QA')
    await user.click(screen.getByRole('button', { name: 'Add mapping' }))
    const row = await screen.findByRole('row', { name: /eng\/ops team/ })
    expect(requests.find((r) => r.method === 'PUT' && r.path === '/api/v1/admin/idp-mappings/eng%2Fops%20team')?.body).toEqual({
      group_id: 2,
    })

    await user.selectOptions(within(row).getByLabelText('Yumdrake group for eng/ops team'), 'Administrators')
    await waitFor(() => expect(mockState().mappings.find((m) => m.idp_group === 'eng/ops team')?.group_id).toBe(1))

    await user.click(within(row).getByRole('button', { name: 'Remove mapping' }))
    await user.click(within(row).getByRole('button', { name: 'Confirm remove' }))
    await waitFor(() => expect(screen.queryByRole('row', { name: /eng\/ops team/ })).not.toBeInTheDocument())
  })

  it('shows a loading state, then an error with retry', async () => {
    delayNext('GET', '/api/v1/admin/groups', 50)
    failNext('GET', '/api/v1/admin/grants', 500, 'internal', 'The grants could not be read')
    const user = userEvent.setup()
    renderApp(<App />, { route: '/admin/groups', me: accessManagerMe })
    expect(await screen.findByText('Loading')).toBeInTheDocument()
    expect(await screen.findByRole('alert')).toHaveTextContent('The grants could not be read')
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('region', { name: 'QA' })).toBeInTheDocument()
  })

  it('is absent, and nothing is requested for it, without manage_access', async () => {
    renderApp(<App />, { route: '/admin/groups', me: viewerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole('tab', { name: 'Groups and permissions' })).not.toBeInTheDocument()
    const sectionRoutes = ['/admin/groups', '/admin/grants', '/admin/idp-mappings']
    expect(requests.filter((r) => sectionRoutes.some((route) => r.path.startsWith(route)))).toEqual([])
  })
})
