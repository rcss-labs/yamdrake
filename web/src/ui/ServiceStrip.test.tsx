import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ServiceStrip } from './ServiceStrip'

describe('ServiceStrip', () => {
  it('is an image named by its label', () => {
    render(
      <ServiceStrip
        label="12 services: 10 succeeded, 2 failed"
        cells={[{ status: 'succeeded', title: 'orders: Succeeded' }]}
      />,
    )
    expect(screen.getByRole('img', { name: '12 services: 10 succeeded, 2 failed' })).toBeInTheDocument()
  })

  it('has one titled cell per item, with group breaks marked', () => {
    const { container } = render(
      <ServiceStrip
        label="Services"
        cells={[
          { status: 'succeeded', title: 'orders: Succeeded' },
          { status: 'failed', title: 'billing: Failed', gapBefore: true },
        ]}
      />,
    )
    const cells = container.querySelectorAll('[title]')
    expect(cells).toHaveLength(2)
    expect(cells[1]).toHaveAttribute('title', 'billing: Failed')
    expect(cells[1]).toHaveAttribute('data-gap', 'true')
    expect(cells[0]).not.toHaveAttribute('data-gap')
  })
})
