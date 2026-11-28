import type { ComponentPropsWithoutRef } from 'react'
import { ApiError } from '../api/error'
import { Button } from './Button'
import { cx } from './classnames'
import './ErrorState.css'

export interface ErrorStateProps extends ComponentPropsWithoutRef<'div'> {
  error: ApiError | Error
  onRetry?: () => void
}

export function ErrorState({ error, onRetry, className, ...rest }: ErrorStateProps) {
  const unreachable = error instanceof ApiError && error.code === 'network'
  return (
    <div className={cx('ui-error', className)} role="alert" {...rest}>
      <h2>{unreachable ? 'Could not reach the server' : 'Something went wrong'}</h2>
      <p>{unreachable ? 'Check your connection, then try again.' : error.message}</p>
      {onRetry && <Button onClick={onRetry}>Try again</Button>}
    </div>
  )
}
