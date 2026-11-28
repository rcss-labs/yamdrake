import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { SearchBox } from './SearchBox'

describe('SearchBox', () => {
  it('uses its label as name and placeholder and reports typing', async () => {
    const onChange = vi.fn()
    render(<SearchBox label="Search jobs" value="" onChange={onChange} />)
    const input = screen.getByRole('searchbox', { name: 'Search jobs' })
    expect(input).toHaveAttribute('placeholder', 'Search jobs')
    await userEvent.type(input, 'a')
    expect(onChange).toHaveBeenCalledWith('a')
  })
})
