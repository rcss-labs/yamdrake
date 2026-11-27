import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import { statusLabel, statusShape, statusTone, type StatusKey } from './status'
import './StatusPill.css'

export interface StatusPillProps extends Omit<ComponentPropsWithoutRef<'span'>, 'children'> {
  status: StatusKey
  label?: string
}

// Colour is never the only signal: every status draws a shape and spells out a label.
export function StatusPill({ status, label, className, ...rest }: StatusPillProps) {
  return (
    <span className={cx('ui-pill', className)} data-tone={statusTone(status)} {...rest}>
      <span className="ui-shape" data-shape={statusShape(status)} aria-hidden="true" />
      {label ?? statusLabel(status)}
    </span>
  )
}
