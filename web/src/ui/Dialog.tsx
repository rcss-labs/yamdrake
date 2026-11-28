import { useEffect, useId, useRef } from 'react'
import type { KeyboardEvent } from 'react'
import { Button } from './Button'
import './Dialog.css'

export interface ConfirmDialogProps {
  open: boolean
  title: string
  /** One sentence: what will happen and what will not. */
  body: string
  confirmLabel: string
  /** A destructive or far-reaching action: the confirm button looks the part. */
  danger?: boolean
  onConfirm(): void
  onCancel(): void
}

const focusable = 'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'

/**
 * A modal confirmation on the native <dialog>. Focus starts on the safe button (Go back), Tab and Shift+Tab stay
 * inside, Escape and a click outside cancel, and focus returns to what had it before the dialog opened.
 * The native dialog already makes the page inert in a browser; the Tab handling below also keeps focus in
 * place where it does not (and in tests).
 */
export function ConfirmDialog({ open, title, body, confirmLabel, danger = false, onConfirm, onCancel }: ConfirmDialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const cancelRef = useRef<HTMLButtonElement>(null)
  const titleId = useId()
  const bodyId = useId()
  // Confirm acts once per opening, so a double click cannot run the action twice.
  const confirmed = useRef(false)
  // Escape arrives as a key press and then as the native cancel event; only the first one counts.
  const escapeSeen = useRef(false)

  useEffect(() => {
    const dialog = dialogRef.current
    if (!open || !dialog) return undefined
    confirmed.current = false
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null
    dialog.showModal()
    cancelRef.current?.focus()
    return () => {
      dialog.close()
      before?.focus()
    }
  }, [open])

  if (!open) return null

  const keepFocusInside = (event: KeyboardEvent<HTMLDialogElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      escapeSeen.current = true
      setTimeout(() => {
        escapeSeen.current = false
      }, 0)
      onCancel()
      return
    }
    if (event.key !== 'Tab') return
    const items = Array.from(event.currentTarget.querySelectorAll<HTMLElement>(focusable))
    const first = items[0]
    const last = items[items.length - 1]
    if (!first || !last) return
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

  return (
    // The dialog element handles Escape and Tab itself; the click closes only when it lands on the backdrop.
    // eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions
    <dialog
      ref={dialogRef}
      className="ui-dialog"
      aria-labelledby={titleId}
      aria-describedby={bodyId}
      onKeyDown={keepFocusInside}
      onCancel={(event) => {
        // Keep the native close from happening: React decides by `open`.
        event.preventDefault()
        if (!escapeSeen.current) onCancel()
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return
        // The dialog's own padding is part of the dialog; only a click outside its box is the backdrop.
        const box = event.currentTarget.getBoundingClientRect()
        const outside = event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom
        if (outside) onCancel()
      }}
    >
      <h2 id={titleId} className="ui-dialog-title">
        {title}
      </h2>
      <p id={bodyId} className="ui-dialog-body">
        {body}
      </p>
      <div className="ui-dialog-actions">
        <button ref={cancelRef} type="button" className="ui-btn" onClick={onCancel}>
          <span className="ui-btn-label">Go back</span>
        </button>
        <Button
          variant={danger ? 'danger' : 'primary'}
          onClick={() => {
            if (confirmed.current) return
            confirmed.current = true
            onConfirm()
          }}
        >
          {confirmLabel}
        </Button>
      </div>
    </dialog>
  )
}
