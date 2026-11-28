import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './Button.css'

export interface ButtonProps extends ComponentPropsWithoutRef<'button'> {
  variant?: 'default' | 'primary' | 'danger'
  size?: 'md' | 'sm'
  loading?: boolean
}

export function Button({
  variant = 'default',
  size = 'md',
  loading = false,
  type = 'button',
  disabled,
  className,
  children,
  ...rest
}: ButtonProps) {
  return (
    <button
      type={type}
      className={cx('ui-btn', `ui-btn-${variant}`, size === 'sm' && 'ui-btn-sm', loading && 'ui-btn-loading', className)}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}
    >
      {/* The label stays in the layout while loading, so the button keeps its width. */}
      <span className="ui-btn-label">{children}</span>
      {loading && <span className="ui-btn-spinner" aria-hidden="true" />}
    </button>
  )
}
