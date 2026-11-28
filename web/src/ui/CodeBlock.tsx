import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './CodeBlock.css'

export interface CodeBlockProps extends ComponentPropsWithoutRef<'pre'> {
  // Accessible name of the scrollable region.
  label: string
}

export function CodeBlock({ label, className, children, ...rest }: CodeBlockProps) {
  // A scrollable region must be focusable so keyboard users can scroll it.
  return (
    // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
    <pre className={cx('ui-code', className)} tabIndex={0} role="region" aria-label={label} {...rest}>
      <code>{children}</code>
    </pre>
  )
}
