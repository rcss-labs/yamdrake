import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/error'
import { ErrorState } from './ErrorState'

describe('ErrorState', () => {
  it('shows the server message and retries', async () => {
    const onRetry = vi.fn()
    render(<ErrorState error={new ApiError(500, 'internal', 'The database is unavailable')} onRetry={onRetry} />)
    expect(screen.getByRole('alert')).toHaveTextContent('The database is unavailable')
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('explains a network failure in plain words', () => {
    render(<ErrorState error={new ApiError(0, 'network', 'Failed to fetch')} onRetry={() => {}} />)
    expect(screen.getByRole('alert')).toHaveTextContent('Could not reach the server')
  })

  it('accepts a plain Error and omits retry when none is given', () => {
    render(<ErrorState error={new Error('Something broke')} />)
    expect(screen.getByRole('alert')).toHaveTextContent('Something broke')
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('ApiError carries status and code', () => {
    const error = new ApiError(403, 'forbidden', 'No')
    expect(error).toBeInstanceOf(Error)
    expect([error.name, error.status, error.code, error.message]).toEqual(['ApiError', 403, 'forbidden', 'No'])
  })
})
