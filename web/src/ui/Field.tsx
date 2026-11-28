import { cloneElement, useId, type ComponentPropsWithoutRef, type ReactElement } from 'react'
import { cx } from './classnames'
import './Field.css'

interface ControlProps {
  id?: string
  'aria-describedby'?: string
  'aria-invalid'?: boolean
}

export interface FieldProps extends Omit<ComponentPropsWithoutRef<'div'>, 'children'> {
  label: string
  hint?: string
  error?: string
  children: ReactElement<ControlProps>
}

// Wraps exactly one control and injects the id and aria attributes that tie
// the label, hint and error to it.
export function Field({ label, hint, error, children, className, ...rest }: FieldProps) {
  const generated = useId()
  const controlId = children.props.id ?? `${generated}-control`
  const hintId = `${generated}-hint`
  const errorId = `${generated}-error`
  const describedBy = [children.props['aria-describedby'], hint ? hintId : undefined, error ? errorId : undefined]
    .filter(Boolean)
    .join(' ')

  return (
    <div className={cx('ui-field', className)} {...rest}>
      <label className="ui-field-label" htmlFor={controlId}>
        {label}
      </label>
      {cloneElement(children, {
        id: controlId,
        'aria-describedby': describedBy || undefined,
        'aria-invalid': error ? true : undefined,
      })}
      {hint && (
        <p className="ui-field-hint" id={hintId}>
          {hint}
        </p>
      )}
      {error && (
        <p className="ui-field-error" id={errorId} role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
