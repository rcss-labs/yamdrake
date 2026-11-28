import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Time } from './index'
import { exactTime } from './time'

describe('Time', () => {
  it('shows words and keeps the exact time in the tooltip', () => {
    const iso = new Date(Date.now() - 3 * 60_000).toISOString()
    render(<Time iso={iso} />)
    const element = screen.getByText('3 minutes ago')
    expect(element).toHaveAttribute('title', exactTime(iso))
    expect(element).toHaveAttribute('datetime', iso)
  })

  it('shows the fallback when there is no time', () => {
    render(<Time iso={null} fallback="Not started" />)
    expect(screen.getByText('Not started')).toBeInTheDocument()
  })
})
