import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import { statusTone, type StatusKey } from './status'
import './ServiceStrip.css'

export interface ServiceCell {
  status: StatusKey
  title: string
  // Starts a new group: leaves a gap before this cell.
  gapBefore?: boolean
}

export interface ServiceStripProps extends Omit<ComponentPropsWithoutRef<'span'>, 'children'> {
  cells: ServiceCell[]
  size?: 'sm' | 'lg'
  // Summary read out for the whole strip, e.g. "12 services: 10 succeeded, 2 failed".
  label: string
}

export function ServiceStrip({ cells, size = 'sm', label, className, ...rest }: ServiceStripProps) {
  return (
    <span role="img" aria-label={label} className={cx('ui-strip', size === 'lg' && 'ui-strip-lg', className)} {...rest}>
      {cells.map((cell, index) => (
        <span
          key={index}
          className="ui-strip-cell"
          data-status={cell.status}
          data-tone={statusTone(cell.status)}
          data-gap={cell.gapBefore ? 'true' : undefined}
          title={cell.title}
        />
      ))}
    </span>
  )
}
