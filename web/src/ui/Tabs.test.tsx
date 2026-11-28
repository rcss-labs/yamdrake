import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { Tabs } from './Tabs'

const tabs = [
  { id: 'overview', label: 'Overview' },
  { id: 'definition', label: 'Definition' },
  { id: 'history', label: 'History' },
]

function Controlled({ onChange }: { onChange?: (id: string) => void }) {
  const [active, setActive] = useState('overview')
  return (
    <Tabs
      label="Job sections"
      tabs={tabs}
      active={active}
      onChange={(id) => {
        setActive(id)
        onChange?.(id)
      }}
    />
  )
}

describe('Tabs', () => {
  it('exposes tablist, tab and selection roles', () => {
    render(<Tabs label="Job sections" tabs={tabs} active="definition" onChange={() => {}} />)
    expect(screen.getByRole('tablist', { name: 'Job sections' })).toBeInTheDocument()
    expect(screen.getAllByRole('tab')).toHaveLength(3)
    expect(screen.getByRole('tab', { name: 'Definition' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'false')
  })

  it('only the selected tab is in the tab order', () => {
    render(<Tabs label="Job sections" tabs={tabs} active="definition" onChange={() => {}} />)
    expect(screen.getByRole('tab', { name: 'Definition' })).toHaveAttribute('tabindex', '0')
    expect(screen.getByRole('tab', { name: 'History' })).toHaveAttribute('tabindex', '-1')
  })

  it('reports a click', async () => {
    const onChange = vi.fn()
    render(<Controlled onChange={onChange} />)
    await userEvent.click(screen.getByRole('tab', { name: 'History' }))
    expect(onChange).toHaveBeenCalledWith('history')
  })

  it('moves and selects with arrow keys, wrapping at the ends', async () => {
    render(<Controlled />)
    screen.getByRole('tab', { name: 'Overview' }).focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(screen.getByRole('tab', { name: 'Definition' })).toHaveFocus()
    expect(screen.getByRole('tab', { name: 'Definition' })).toHaveAttribute('aria-selected', 'true')
    await userEvent.keyboard('{ArrowLeft}{ArrowLeft}')
    expect(screen.getByRole('tab', { name: 'History' })).toHaveFocus()
    expect(screen.getByRole('tab', { name: 'History' })).toHaveAttribute('aria-selected', 'true')
  })

  it('jumps with Home and End', async () => {
    render(<Controlled />)
    screen.getByRole('tab', { name: 'Overview' }).focus()
    await userEvent.keyboard('{End}')
    expect(screen.getByRole('tab', { name: 'History' })).toHaveFocus()
    await userEvent.keyboard('{Home}')
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveFocus()
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true')
  })

  it('points each tab at its panel when asked', () => {
    render(
      <Tabs label="Job sections" tabs={tabs} active="overview" onChange={() => {}} panelId={(id) => `panel-${id}`} />,
    )
    expect(screen.getByRole('tab', { name: 'History' })).toHaveAttribute('aria-controls', 'panel-history')
  })
})
