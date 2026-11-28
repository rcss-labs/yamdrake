import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './Segmented.css'

export interface SegmentedOption {
  value: string
  label: string
}

export interface SegmentedProps extends Omit<ComponentPropsWithoutRef<'div'>, 'onChange'> {
  options: SegmentedOption[]
  value: string
  onChange: (value: string) => void
  label: string
}

export function Segmented({ options, value, onChange, label, className, ...rest }: SegmentedProps) {
  return (
    <div role="group" aria-label={label} className={cx('ui-seg', className)} {...rest}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          aria-pressed={option.value === value}
          className={cx(option.value === value && 'ui-seg-on')}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}
