import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './controls.css'

export interface SelectProps extends Omit<ComponentPropsWithoutRef<'select'>, 'size'> {
  size?: 'md' | 'sm'
}

export function Select({ className, size = 'md', ...rest }: SelectProps) {
  return <select className={cx('ui-select', size === 'sm' && 'ui-select-sm', className)} {...rest} />
}
