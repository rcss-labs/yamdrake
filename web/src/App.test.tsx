import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { App } from './App'
import { adminMe } from './test/fixtures'
import { renderApp } from './test/render'

describe('App', () => {
  it('shows the product name and who is signed in', async () => {
    renderApp(<App />, { me: adminMe })
    expect(await screen.findByText(`Signed in as ${adminMe.name}`)).toBeInTheDocument()
    expect(screen.getByText('Yumdrake')).toBeInTheDocument()
  })

  it('shows the sign-in page when nobody is signed in', async () => {
    renderApp(<App />, { me: null })
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.getByText('Yumdrake')).toBeInTheDocument()
    expect(screen.queryByText(/Signed in as/)).not.toBeInTheDocument()
  })
})
