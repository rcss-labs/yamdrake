import type { ComponentPropsWithoutRef, ReactNode } from 'react'
import { cx } from './classnames'
import './EmptyState.css'

export interface EmptyStateProps extends Omit<ComponentPropsWithoutRef<'div'>, 'title'> {
  title: ReactNode
  // Says what to do next.
  children?: ReactNode
  action?: ReactNode
}

export function EmptyState({ title, children, action, className, ...rest }: EmptyStateProps) {
  return (
    <div className={cx('ui-empty', className)} {...rest}>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
      {action}
    </div>
  )
}
