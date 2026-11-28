import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { exactTime } from '../../ui'
import { renderApp } from '../../test/render'
import { adminMe, mockState } from '../../test/server'
import { FreezeBanner } from './FreezeBanner'

const until = exactTime('2026-11-30T18:00:00Z')

describe('the freeze banner', () => {
  it('names the label, the time and the reason', async () => {
    renderApp(<FreezeBanner label="prod" />, { me: adminMe })
    expect(await screen.findByText(`Deployments to prod are frozen until ${until}: Quarter-end change freeze`)).toBeInTheDocument()
  })

  it('names the environment of an environment freeze', async () => {
    mockState().freezes.push({
      id: 3,
      label: '',
      environment: 'hetzner-01',
      reason: 'Disk swap',
      created_by_name: 'Dana Levi',
      created_at: '2026-11-27T08:00:00Z',
      expires_at: '2026-11-28T08:00:00Z',
      ended_at: null,
      ended_by_name: '',
    })
    renderApp(<FreezeBanner environment="hetzner-01" label="dev" />, { me: adminMe })
    expect(await screen.findByText(`Deployments to hetzner-01 are frozen until ${exactTime('2026-11-28T08:00:00Z')}: Disk swap`)).toBeInTheDocument()
    expect(screen.queryByText(/Quarter-end/)).not.toBeInTheDocument()
  })

  it('shows one banner per matching freeze', async () => {
    mockState().freezes.push({
      id: 3,
      label: '',
      environment: 'prod-01',
      reason: 'Disk swap',
      created_by_name: 'Dana Levi',
      created_at: '2026-11-27T08:00:00Z',
      expires_at: '2026-11-28T08:00:00Z',
      ended_at: null,
      ended_by_name: '',
    })
    renderApp(<FreezeBanner environment="prod-01" label="prod" />, { me: adminMe })
    expect(await screen.findAllByText(/are frozen until/)).toHaveLength(2)
  })

  it('shows nothing when no active freeze matches, and ignores ended ones', async () => {
    const { container } = renderApp(<FreezeBanner environment="preprod-01" label="preprod" />, { me: adminMe })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByText(/are frozen/)).not.toBeInTheDocument()
    expect(container.querySelector('.ui-note')).toBeNull()
  })
})
