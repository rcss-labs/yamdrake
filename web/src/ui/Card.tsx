import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './Card.css'

export interface CardProps extends ComponentPropsWithoutRef<'div'> {
  // Removes padding so a table can run edge to edge.
  flush?: boolean
}

export function Card({ flush = false, className, ...rest }: CardProps) {
  return <div className={cx('ui-card', flush && 'ui-card-flush', className)} {...rest} />
}
