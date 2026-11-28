import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Field } from './Field'
import { TextInput } from './TextInput'

describe('Field', () => {
  it('lets the control be found by its label', () => {
    render(
      <Field label="Folder name">
        <TextInput />
      </Field>,
    )
    expect(screen.getByLabelText('Folder name')).toBeInstanceOf(HTMLInputElement)
  })

  it('describes the control with its hint', () => {
    render(
      <Field label="Folder name" hint="Letters and hyphens only">
        <TextInput />
      </Field>,
    )
    expect(screen.getByLabelText('Folder name')).toHaveAccessibleDescription('Letters and hyphens only')
  })

  it('marks the control invalid and announces the error', () => {
    render(
      <Field label="Folder name" hint="Letters and hyphens only" error="That name is taken">
        <TextInput />
      </Field>,
    )
    const input = screen.getByLabelText('Folder name')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAccessibleDescription('Letters and hyphens only That name is taken')
    expect(screen.getByRole('alert')).toHaveTextContent('That name is taken')
  })

  it('is not invalid without an error', () => {
    render(
      <Field label="Folder name">
        <TextInput />
      </Field>,
    )
    expect(screen.getByLabelText('Folder name')).not.toHaveAttribute('aria-invalid')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
