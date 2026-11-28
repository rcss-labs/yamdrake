import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Loading } from './Loading'

describe('Loading', () => {
  it('announces loading to assistive technology', () => {
    render(<Loading />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading')
  })
})
