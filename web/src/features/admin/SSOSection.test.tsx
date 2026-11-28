import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { adminMe, delayNext, failNext, mockState, requests, viewerMe } from '../../test/server'

async function openSso() {
  const rendered = renderApp(<App />, { route: '/admin/sso', me: adminMe })
  await screen.findByRole('switch', { name: 'Single sign-on enabled' })
  return rendered
}

const putsToSso = () => requests.filter((r) => r.method === 'PUT' && r.path === '/api/v1/admin/sso')

afterEach(() => {
  vi.restoreAllMocks()
})

describe('the single sign-on section', () => {
  it('shows the stored settings', async () => {
    await openSso()
    expect(screen.getByRole('switch', { name: 'Single sign-on enabled' })).toBeChecked()
    expect(screen.getByLabelText('Identity provider metadata (XML)')).toHaveValue(mockState().sso.idp_metadata_xml)
    expect(screen.getByLabelText('Service provider entity ID')).toHaveValue('https://yumdrake.example.com/saml/metadata')
    expect(screen.getByLabelText('Groups attribute')).toHaveValue('groups')
    expect(screen.getByLabelText('Email attribute')).toHaveValue('')
    expect(screen.getByLabelText('Name attribute')).toHaveValue('displayName')
    expect(screen.getByText('https://yumdrake.example.com/saml/metadata', { selector: '.sso-url' })).toBeInTheDocument()
  })

  it('saves changed settings with PUT and confirms', async () => {
    const user = userEvent.setup()
    await openSso()
    await user.click(screen.getByRole('switch', { name: 'Single sign-on enabled' }))
    const groups = screen.getByLabelText('Groups attribute')
    await user.clear(groups)
    await user.type(groups, 'memberOf')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Settings saved')).toBeInTheDocument()
    expect(putsToSso()).toHaveLength(1)
    expect(putsToSso()[0]?.body).toEqual({
      enabled: false,
      idp_metadata_xml: mockState().sso.idp_metadata_xml,
      entity_id: '',
      groups_attribute: 'memberOf',
      email_attribute: '',
      name_attribute: 'displayName',
    })
    expect(mockState().sso.groups_attribute).toBe('memberOf')
  })

  it('sends the typed entity ID when it differs from the default', async () => {
    const user = userEvent.setup()
    await openSso()
    const entityId = screen.getByLabelText('Service provider entity ID')
    await user.clear(entityId)
    await user.type(entityId, 'https://sp.example.com/custom')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Settings saved')).toBeInTheDocument()
    expect(putsToSso()[0]?.body).toMatchObject({ entity_id: 'https://sp.example.com/custom' })
  })

  it('shows the server error next to Save and keeps what was typed', async () => {
    const user = userEvent.setup()
    await openSso()
    const metadata = screen.getByLabelText('Identity provider metadata (XML)')
    await user.clear(metadata)
    await user.type(metadata, 'not xml')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/not valid SAML metadata/)
    expect(metadata).toHaveValue('not xml')
    expect(screen.queryByText('Settings saved')).not.toBeInTheDocument()
  })

  it('tests the connection and shows the result next to the button', async () => {
    const user = userEvent.setup()
    await openSso()
    await user.click(screen.getByRole('button', { name: 'Test' }))
    const result = await screen.findByRole('status')
    expect(result).toHaveTextContent('Test passed')
    expect(result).toHaveTextContent('The metadata is valid and has a signing certificate')
    expect(result).not.toHaveTextContent(/[✓✕]/)
    expect(requests.some((r) => r.method === 'POST' && r.path === '/api/v1/admin/sso/test')).toBe(true)
  })

  it('shows a failed test as a failure', async () => {
    const user = userEvent.setup()
    mockState().sso.idp_metadata_xml = 'garbage'
    await openSso()
    await user.click(screen.getByRole('button', { name: 'Test' }))
    const result = await screen.findByRole('status')
    expect(result).toHaveTextContent('Test failed')
    expect(result).toHaveTextContent('The metadata could not be read')
    expect(result).not.toHaveTextContent(/[✓✕]/)
  })

  it('shows a test request error in place', async () => {
    const user = userEvent.setup()
    await openSso()
    failNext('POST', '/api/v1/admin/sso/test', 403, 'forbidden', 'You may not test sign-in')
    await user.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('You may not test sign-in')
  })

  it('copies the service provider metadata URL', async () => {
    const user = userEvent.setup()
    await openSso()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    await user.click(screen.getByRole('button', { name: 'Copy metadata URL' }))
    expect(writeText).toHaveBeenCalledWith('https://yumdrake.example.com/saml/metadata')
    expect(await screen.findByText('Copied')).toBeInTheDocument()
  })

  it('says to copy by hand when the clipboard is not available', async () => {
    const user = userEvent.setup()
    await openSso()
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    await user.click(screen.getByRole('button', { name: 'Copy metadata URL' }))
    expect(await screen.findByText('Could not copy. Select the address and copy it by hand.')).toBeInTheDocument()
  })

  it('shows a loading state while the settings load', async () => {
    delayNext('GET', '/api/v1/admin/sso', 50)
    renderApp(<App />, { route: '/admin/sso', me: adminMe })
    expect(await screen.findByText('Loading')).toBeInTheDocument()
    expect(await screen.findByRole('switch', { name: 'Single sign-on enabled' })).toBeInTheDocument()
  })

  it('shows a readable error with retry when the settings cannot be read', async () => {
    const user = userEvent.setup()
    failNext('GET', '/api/v1/admin/sso', 500, 'internal', 'Settings could not be read')
    renderApp(<App />, { route: '/admin/sso', me: adminMe })
    expect(await screen.findByRole('alert')).toHaveTextContent('Settings could not be read')
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('switch', { name: 'Single sign-on enabled' })).toBeInTheDocument()
  })

  it('is absent, and nothing is requested for it, without manage_access', async () => {
    renderApp(<App />, { route: '/admin/sso', me: viewerMe })
    expect(await screen.findByText('You do not have access')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole('tab', { name: 'Single sign-on' })).not.toBeInTheDocument()
    expect(requests.filter((r) => r.path.startsWith('/api/v1/admin/sso'))).toEqual([])
  })
})
