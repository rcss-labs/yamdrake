import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Button } from './Button'

describe('Button', () => {
  it('runs its handler when clicked', async () => {
    const onClick = vi.fn()
    render(<Button onClick={onClick}>Add job</Button>)
    await userEvent.click(screen.getByRole('button', { name: 'Add job' }))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('is disabled while loading and keeps its label readable', async () => {
    const onClick = vi.fn()
    render(
      <Button loading onClick={onClick}>
        Create folder
      </Button>,
    )
    const button = screen.getByRole('button', { name: /Create folder/ })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('aria-busy', 'true')
    expect(button).toHaveTextContent('Create folder')
    await userEvent.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('defaults to type button so it never submits a form by accident', () => {
    render(<Button>Cancel</Button>)
    expect(screen.getByRole('button')).toHaveAttribute('type', 'button')
  })
})
