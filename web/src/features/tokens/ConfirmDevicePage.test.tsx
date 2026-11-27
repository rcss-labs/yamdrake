import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '../../App'
import { renderApp } from '../../test/render'
import { failNext, mockState, requests } from '../../test/server'

const confirmRequests = () => requests.filter((request) => request.path === '/api/v1/cli/login/confirm')

function startDevice() {
  mockState().devices['device-code-1'] = { userCode: 'KQ7M2X11', confirmedBy: null, tokenIssued: false }
}

describe('the device confirm page', () => {
  it('explains a missing code and offers no button', async () => {
    renderApp(<App />, { route: '/cli/confirm' })
    expect(await screen.findByText(/There is no sign-in code in this address/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Confirm this sign-in for the command line' })).not.toBeInTheDocument()
    expect(confirmRequests()).toEqual([])
  })

  it('shows the code and confirms it', async () => {
    startDevice()
    renderApp(<App />, { route: '/cli/confirm?code=KQ7M2X11' })
    expect(await screen.findByText('KQ7M2X11')).toHaveClass('mono')
    await userEvent.click(screen.getByRole('button', { name: 'Confirm this sign-in for the command line' }))

    expect(await screen.findByText(/You can return to the terminal/)).toBeInTheDocument()
    expect(confirmRequests()).toHaveLength(1)
    expect(confirmRequests()[0]?.body).toEqual({ user_code: 'KQ7M2X11' })
    expect(screen.queryByRole('button', { name: 'Confirm this sign-in for the command line' })).not.toBeInTheDocument()
    expect(mockState().devices['device-code-1']?.confirmedBy).not.toBeNull()
  })

  it('shows the server\'s message when the code is refused, and allows another try', async () => {
    startDevice()
    renderApp(<App />, { route: '/cli/confirm?code=KQ7M2X11' })
    const button = await screen.findByRole('button', { name: 'Confirm this sign-in for the command line' })
    failNext('POST', '/api/v1/cli/login/confirm', 404, 'not_found', 'That code has expired. Run the sign-in again.')
    await userEvent.click(button)
    expect(await screen.findByRole('alert')).toHaveTextContent('That code has expired. Run the sign-in again.')
    expect(screen.queryByText(/You can return to the terminal/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Confirm this sign-in for the command line' })).toBeEnabled()
  })

  it('shows the server\'s message for an unknown code', async () => {
    renderApp(<App />, { route: '/cli/confirm?code=NOPE0000' })
    await userEvent.click(await screen.findByRole('button', { name: 'Confirm this sign-in for the command line' }))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(confirmRequests()).toHaveLength(1)
  })
})
