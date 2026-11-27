import type { QueryClient } from '@tanstack/react-query'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { connectionsManagerMe, delayNext, failNext, mockState, requests, viewerMe } from '../../test/server'

const route = '/admin/aws'
const accountsPath = '/api/v1/admin/aws-accounts'

function rowOf(name: string) {
  return screen.getByRole('row', { name: new RegExp(name) })
}

async function openForm() {
  const rendered = renderApp(<App />, { route, me: connectionsManagerMe })
  await screen.findByRole('table', { name: 'AWS accounts' })
  await userEvent.click(screen.getByRole('button', { name: 'Connect AWS account' }))
  return rendered
}

/** Every mutation the query client still remembers, with its variables and data, as one string. */
function rememberedMutations(queryClient: QueryClient) {
  return JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => [mutation.state.variables, mutation.state.data]))
}

async function fillBasics(name: string, accountId: string) {
  await userEvent.type(screen.getByLabelText('Name'), name)
  await userEvent.type(screen.getByLabelText('AWS account ID'), accountId)
}

describe('the AWS accounts section', () => {
  it('lists accounts with how each connects and which environments use them', async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    const development = await screen.findByRole('row', { name: /Development/ })
    expect(within(development).getByText('111111111111')).toBeInTheDocument()
    expect(within(development).getByText('arn:aws:iam::111111111111:role/yumdrake-discovery')).toBeInTheDocument()
    expect(within(development).getByText('arn:aws:iam::111111111111:role/yumdrake-deploy')).toBeInTheDocument()
    expect(within(development).getByText('hetzner-01, hetzner-02, hetzner-03')).toBeInTheDocument()
    const production = rowOf('Production')
    expect(within(production).getByText('Access key ending Q7XA')).toBeInTheDocument()
    expect(within(production).getByText('Access key ending M2PD')).toBeInTheDocument()
    expect(within(production).getByText('prod-01')).toBeInTheDocument()
  })

  it('says None yet for an account no environment uses', async () => {
    mockState().awsAccounts[0]!.environments = []
    renderApp(<App />, { route, me: connectionsManagerMe })
    const development = await screen.findByRole('row', { name: /Development/ })
    expect(within(development).getByText('None yet')).toBeInTheDocument()
  })

  it('shows an empty state that says how to connect the first account', async () => {
    mockState().awsAccounts.length = 0
    renderApp(<App />, { route, me: connectionsManagerMe })
    expect(await screen.findByText('No AWS accounts are connected')).toBeInTheDocument()
    expect(screen.getByText(/Connect an account so Yumdrake can discover and deploy/)).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('shows loading, then a readable error with a retry', async () => {
    delayNext('GET', accountsPath, 50)
    failNext('GET', accountsPath, 500, 'internal', 'The database is down')
    renderApp(<App />, { route, me: connectionsManagerMe })
    expect(await screen.findByText('Loading')).toBeInTheDocument()
    expect(await screen.findByText('The database is down')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table', { name: 'AWS accounts' })).toBeInTheDocument()
  })

  it('tests an account and shows a working connection in its row', async () => {
    renderApp(<App />, { route, me: connectionsManagerMe })
    const development = await screen.findByRole('row', { name: /Development/ })
    await userEvent.click(within(development).getByRole('button', { name: 'Test Development' }))
    expect(await within(rowOf('Development')).findByText(/Connected to account 111111111111/)).toBeInTheDocument()
    expect(requests.some((request) => request.method === 'POST' && request.path === `${accountsPath}/1/test`)).toBe(true)
  })

  it('shows the AWS error code in plain words when the test fails', async () => {
    mockState().awsAccounts.push({ ...mockState().awsAccounts[0]!, id: 9, name: 'Mismatch', account_id: '444444444999', environments: [] })
    renderApp(<App />, { route, me: connectionsManagerMe })
    await screen.findByRole('row', { name: /Mismatch/ })
    await userEvent.click(screen.getByRole('button', { name: 'Test Mismatch' }))
    const result = await within(rowOf('Mismatch')).findByText(/The credentials belong to a different AWS account/)
    expect(result).toBeInTheDocument()
    expect(within(rowOf('Development')).queryByText(/different AWS account/)).not.toBeInTheDocument()
  })

  it('shows a failed test request in the row', async () => {
    failNext('POST', `${accountsPath}/1/test`, 500, 'internal', 'The test could not run')
    renderApp(<App />, { route, me: connectionsManagerMe })
    await userEvent.click(await screen.findByRole('button', { name: 'Test Development' }))
    expect(await within(rowOf('Development')).findByText('The test could not run')).toBeInTheDocument()
  })

  it('posts both credential blocks and clears every secret afterwards', async () => {
    delayNext('POST', accountsPath, 30)
    const { queryClient } = await openForm()
    await fillBasics('Staging', '444444444444')
    await userEvent.type(screen.getByLabelText('Discovery role ARN'), 'arn:aws:iam::444444444444:role/yumdrake-discovery')
    await userEvent.type(screen.getByLabelText('Discovery external ID'), 'staging-ext')
    await userEvent.selectOptions(screen.getByLabelText('Deployment access method'), 'key')
    await userEvent.type(screen.getByLabelText('Deployment access key ID'), 'AKIAEXAMPLE0001')
    const secret = screen.getByLabelText('Deployment secret access key')
    expect(secret).toHaveAttribute('type', 'password')
    await userEvent.type(secret, 'super-secret-value')
    await userEvent.click(screen.getByRole('button', { name: 'Save account' }))

    // While the request is pending the form is still mounted and its secret is already empty.
    expect(screen.getByLabelText('Deployment secret access key')).toHaveValue('')
    expect(await screen.findByRole('row', { name: /Staging/ })).toBeInTheDocument()
    const post = requests.find((request) => request.method === 'POST' && request.path === accountsPath)
    expect(post?.body).toEqual({
      name: 'Staging',
      account_id: '444444444444',
      discovery: { method: 'role', role_arn: 'arn:aws:iam::444444444444:role/yumdrake-discovery', external_id: 'staging-ext' },
      deploy: { method: 'key', access_key_id: 'AKIAEXAMPLE0001', secret_access_key: 'super-secret-value' },
    })
    expect(within(rowOf('Staging')).getByText('Access key ending 0001')).toBeInTheDocument()
    await waitFor(() => expect(rememberedMutations(queryClient)).not.toContain('super-secret-value'))
  })

  it('clears the secrets after a failed save and shows the server error on its field', async () => {
    const { queryClient } = await openForm()
    await fillBasics('Staging', '4444')
    await userEvent.selectOptions(screen.getByLabelText('Discovery access method'), 'key')
    await userEvent.type(screen.getByLabelText('Discovery access key ID'), 'AKIAEXAMPLE0002')
    await userEvent.type(screen.getByLabelText('Discovery secret access key'), 'another-secret')
    await userEvent.type(screen.getByLabelText('Deployment role ARN'), 'arn:aws:iam::444444444444:role/deploy')
    await userEvent.click(screen.getByRole('button', { name: 'Save account' }))

    const accountId = screen.getByLabelText('AWS account ID')
    await waitFor(() => expect(accountId).toHaveAccessibleDescription('account_id must be 12 digits'))
    expect(accountId).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText('Discovery secret access key')).toHaveValue('')
    expect(screen.getByLabelText('Name')).toHaveValue('Staging')
    expect(rememberedMutations(queryClient)).not.toContain('another-secret')
  })

  it('shows a server error about a credential block on that block', async () => {
    await openForm()
    await fillBasics('Staging', '444444444444')
    await userEvent.click(screen.getByRole('button', { name: 'Save account' }))
    const discovery = await screen.findByRole('group', { name: 'Discovery' })
    expect(await within(discovery).findByText('discovery needs a role ARN')).toBeInTheDocument()
  })

  it('shows a conflict in plain words above the form', async () => {
    await openForm()
    await fillBasics('Development', '555555555555')
    await userEvent.type(screen.getByLabelText('Discovery role ARN'), 'arn:a')
    await userEvent.type(screen.getByLabelText('Deployment role ARN'), 'arn:b')
    await userEvent.click(screen.getByRole('button', { name: 'Save account' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('already exists')
  })

  it('shows a 403 from the server as a clear message', async () => {
    await openForm()
    failNext('POST', accountsPath, 403, 'forbidden', 'You do not have permission to do this')
    await fillBasics('Staging', '444444444444')
    await userEvent.type(screen.getByLabelText('Discovery role ARN'), 'arn:a')
    await userEvent.type(screen.getByLabelText('Deployment role ARN'), 'arn:b')
    await userEvent.click(screen.getByRole('button', { name: 'Save account' }))
    expect(await screen.findByText('You do not have permission to do this')).toBeInTheDocument()
  })

  it('has no tab and makes no admin request for a person without manage_connections', async () => {
    renderApp(<App />, { route, me: viewerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.queryByRole('tab', { name: 'AWS accounts' })).not.toBeInTheDocument()
    expect(requests.filter((request) => request.path.startsWith('/api/v1/admin/'))).toEqual([])
  })
})
