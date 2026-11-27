import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './controls.css'

export type TextInputProps = ComponentPropsWithoutRef<'input'>

export function TextInput({ className, type = 'text', ...rest }: TextInputProps) {
  return <input type={type} className={cx('ui-input', className)} {...rest} />
}
