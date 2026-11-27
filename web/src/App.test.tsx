import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { App } from './App'
import { renderApp } from './test/render'

describe('App', () => {
  it('renders the product name', () => {
    renderApp(<App />)
    expect(screen.getByText('Yumdrake')).toBeInTheDocument()
  })
})
