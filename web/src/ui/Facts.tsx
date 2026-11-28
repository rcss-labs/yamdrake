import type { ComponentPropsWithoutRef, ReactNode } from 'react'
import { cx } from './classnames'
import './Facts.css'

export interface Fact {
  label: string
  value: ReactNode
}

export interface FactsProps extends Omit<ComponentPropsWithoutRef<'dl'>, 'children'> {
  items: Fact[]
}

export function Facts({ items, className, ...rest }: FactsProps) {
  return (
    <dl className={cx('ui-facts', className)} {...rest}>
      {items.map((item) => (
        <div key={item.label}>
          <dt>{item.label}</dt>
          <dd>{item.value}</dd>
        </div>
      ))}
    </dl>
  )
}
