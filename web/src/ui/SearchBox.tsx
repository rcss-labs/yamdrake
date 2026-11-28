import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import { Icon } from './Icon'
import './SearchBox.css'

export interface SearchBoxProps extends Omit<ComponentPropsWithoutRef<'input'>, 'value' | 'onChange' | 'type'> {
  value: string
  onChange: (value: string) => void
  label: string
}

export function SearchBox({ value, onChange, label, className, ...rest }: SearchBoxProps) {
  return (
    <div className={cx('ui-search', className)}>
      <Icon name="search" />
      <input
        type="search"
        aria-label={label}
        placeholder={label}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        {...rest}
      />
    </div>
  )
}
