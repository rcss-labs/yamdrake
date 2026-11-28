import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Segmented } from './Segmented'

const options = [
  { value: 'all', label: 'All' },
  { value: 'failed', label: 'Failed' },
]

describe('Segmented', () => {
  it('shows which option is selected with aria-pressed', () => {
    render(<Segmented label="Status" options={options} value="failed" onChange={() => {}} />)
    expect(screen.getByRole('button', { name: 'Failed' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('reports the chosen value on click', async () => {
    const onChange = vi.fn()
    render(<Segmented label="Status" options={options} value="all" onChange={onChange} />)
    await userEvent.click(screen.getByRole('button', { name: 'Failed' }))
    expect(onChange).toHaveBeenCalledWith('failed')
  })

  it('works from the keyboard', async () => {
    const onChange = vi.fn()
    render(<Segmented label="Status" options={options} value="all" onChange={onChange} />)
    await userEvent.tab()
    await userEvent.tab()
    expect(screen.getByRole('button', { name: 'Failed' })).toHaveFocus()
    await userEvent.keyboard('{Enter}')
    expect(onChange).toHaveBeenCalledWith('failed')
  })

  it('names the group', () => {
    render(<Segmented label="Status" options={options} value="all" onChange={() => {}} />)
    expect(screen.getByRole('group', { name: 'Status' })).toBeInTheDocument()
  })
})
