import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './Tag.css'

export function Tag({ className, ...rest }: ComponentPropsWithoutRef<'span'>) {
  return <span className={cx('ui-tag', className)} {...rest} />
}
