import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog } from './Dialog'

function renderDialog(props: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
  const onConfirm = vi.fn()
  const onCancel = vi.fn()
  render(
    <>
      <button type="button">Outside before</button>
      <ConfirmDialog
        open
        title="Cancel this run?"
        body="Steps that have not started will not run."
        confirmLabel="Cancel run"
        danger
        onConfirm={onConfirm}
        onCancel={onCancel}
        {...props}
      />
      <button type="button">Outside after</button>
    </>,
  )
  return { onConfirm, onCancel }
}

describe('ConfirmDialog', () => {
  it('names the dialog by its title and shows the sentence and both buttons', () => {
    renderDialog()
    const dialog = screen.getByRole('dialog', { name: 'Cancel this run?' })
    expect(dialog).toHaveTextContent('Steps that have not started will not run.')
    expect(screen.getByRole('button', { name: 'Cancel run' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Go back' })).toBeInTheDocument()
  })

  it('renders nothing while closed', () => {
    renderDialog({ open: false })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('puts focus inside and keeps Tab and Shift+Tab inside', async () => {
    renderDialog()
    const dialog = screen.getByRole('dialog')
    expect(dialog).toContainElement(document.activeElement as HTMLElement)
    for (let presses = 0; presses < 5; presses += 1) {
      await userEvent.tab()
      expect(dialog).toContainElement(document.activeElement as HTMLElement)
    }
    for (let presses = 0; presses < 5; presses += 1) {
      await userEvent.tab({ shift: true })
      expect(dialog).toContainElement(document.activeElement as HTMLElement)
    }
  })

  it('calls onCancel on Escape and does not confirm', async () => {
    const { onConfirm, onCancel } = renderDialog()
    await userEvent.keyboard('{Escape}')
    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('calls onConfirm once when the confirm button is pressed, and onCancel for the other button', async () => {
    const { onConfirm, onCancel } = renderDialog()
    await userEvent.click(screen.getByRole('button', { name: 'Cancel run' }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(onCancel).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Go back' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('returns focus to the control that was focused before it opened', async () => {
    const onCancel = vi.fn()
    const { rerender } = render(
      <>
        <button type="button">Opener</button>
        <ConfirmDialog open={false} title="T" body="B" confirmLabel="Go" onConfirm={() => undefined} onCancel={onCancel} />
      </>,
    )
    screen.getByRole('button', { name: 'Opener' }).focus()
    rerender(
      <>
        <button type="button">Opener</button>
        <ConfirmDialog open title="T" body="B" confirmLabel="Go" onConfirm={() => undefined} onCancel={onCancel} />
      </>,
    )
    expect(screen.getByRole('dialog')).toContainElement(document.activeElement as HTMLElement)
    rerender(
      <>
        <button type="button">Opener</button>
        <ConfirmDialog open={false} title="T" body="B" confirmLabel="Go" onConfirm={() => undefined} onCancel={onCancel} />
      </>,
    )
    expect(screen.getByRole('button', { name: 'Opener' })).toHaveFocus()
  })
})
