import type { ComponentPropsWithoutRef, ReactNode } from 'react'
import { cx } from './classnames'
import './controls.css'

export interface CheckboxProps extends Omit<ComponentPropsWithoutRef<'input'>, 'type'> {
  label: ReactNode
}

export function Checkbox({ label, className, ...rest }: CheckboxProps) {
  return (
    <label className={cx('ui-check', className)}>
      <input type="checkbox" {...rest} />
      <span>{label}</span>
    </label>
  )
}
