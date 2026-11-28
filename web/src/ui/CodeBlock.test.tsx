import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { CodeBlock } from './CodeBlock'

describe('CodeBlock', () => {
  it('is a labelled region that keyboard users can reach to scroll it sideways', async () => {
    render(<CodeBlock label="Service definition">{'name: a-very-long-line'}</CodeBlock>)
    const region = screen.getByRole('region', { name: 'Service definition' })
    expect(region).toHaveTextContent('name: a-very-long-line')
    await userEvent.setup().tab()
    expect(region).toHaveFocus()
  })
})
