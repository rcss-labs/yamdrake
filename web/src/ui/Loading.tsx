import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './Loading.css'

export function Loading({ className, ...rest }: Omit<ComponentPropsWithoutRef<'div'>, 'children'>) {
  return (
    <div role="status" className={cx('ui-loading', className)} {...rest}>
      <span className="ui-loading-bar" aria-hidden="true" />
      <span className="ui-visually-hidden">Loading</span>
    </div>
  )
}
