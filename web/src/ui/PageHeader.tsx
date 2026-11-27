import type { ComponentPropsWithoutRef, ReactNode } from 'react'
import { cx } from './classnames'
import './PageHeader.css'

export interface PageHeaderProps extends Omit<ComponentPropsWithoutRef<'header'>, 'title'> {
  title: ReactNode
  lead?: ReactNode
  actions?: ReactNode
  // Breadcrumb content, e.g. links. Rendered in a labelled nav.
  crumbs?: ReactNode
}

export function PageHeader({ title, lead, actions, crumbs, className, ...rest }: PageHeaderProps) {
  return (
    <>
      {crumbs && (
        <nav className="ui-crumbs" aria-label="Breadcrumb">
          {crumbs}
        </nav>
      )}
      <header className={cx('ui-ph', className)} {...rest}>
        <div>
          <h1>{title}</h1>
          {lead && <p className="ui-lead">{lead}</p>}
        </div>
        {actions && <div className="ui-actions">{actions}</div>}
      </header>
    </>
  )
}
