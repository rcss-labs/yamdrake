import { useId, useRef, type ComponentPropsWithoutRef, type KeyboardEvent } from 'react'
import { cx } from './classnames'
import './Tabs.css'

export interface TabItem {
  id: string
  label: string
}

export interface TabsProps extends Omit<ComponentPropsWithoutRef<'div'>, 'onChange'> {
  tabs: TabItem[]
  active: string
  onChange: (id: string) => void
  label: string
  // Screens render their own tabpanel; this returns that panel's element id for aria-controls.
  panelId?: (tabId: string) => string
}

// WAI-ARIA tabs with automatic activation: arrow keys move focus and select.
export function Tabs({ tabs, active, onChange, label, panelId, className, ...rest }: TabsProps) {
  const baseId = useId()
  const refs = useRef(new Map<string, HTMLButtonElement>())

  function move(toIndex: number) {
    const target = tabs[(toIndex + tabs.length) % tabs.length]
    if (!target) return
    refs.current.get(target.id)?.focus()
    onChange(target.id)
  }

  function onKeyDown(event: KeyboardEvent, index: number) {
    if (event.key === 'ArrowRight') move(index + 1)
    else if (event.key === 'ArrowLeft') move(index - 1)
    else if (event.key === 'Home') move(0)
    else if (event.key === 'End') move(tabs.length - 1)
    else return
    event.preventDefault()
  }

  return (
    <div role="tablist" aria-label={label} className={cx('ui-tabs', className)} {...rest}>
      {tabs.map((tab, index) => {
        const selected = tab.id === active
        return (
          <button
            key={tab.id}
            ref={(node) => {
              if (node) refs.current.set(tab.id, node)
              else refs.current.delete(tab.id)
            }}
            type="button"
            role="tab"
            id={`${baseId}-${tab.id}`}
            aria-selected={selected}
            aria-controls={panelId?.(tab.id)}
            tabIndex={selected ? 0 : -1}
            className={cx('ui-tab', selected && 'ui-tab-on')}
            onClick={() => onChange(tab.id)}
            onKeyDown={(event) => onKeyDown(event, index)}
          >
            {tab.label}
          </button>
        )
      })}
    </div>
  )
}
