import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ProblemList } from './ProblemList'

describe('ProblemList', () => {
  it('shows file:line and the message for each problem', () => {
    render(
      <ProblemList
        problems={[
          { file: 'services/orders.yaml', line: 14, message: 'unknown service type' },
          { file: 'services/billing.yaml', line: 3, message: 'missing name' },
        ]}
      />,
    )
    expect(screen.getByText('services/orders.yaml:14')).toBeInTheDocument()
    expect(screen.getByText('unknown service type')).toBeInTheDocument()
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
  })

  it('shows the file name alone for a problem about the whole file (line 0)', () => {
    render(<ProblemList problems={[{ file: 'environments.yaml', line: 0, message: 'file is empty' }]} />)
    expect(screen.getByText('environments.yaml')).toBeInTheDocument()
    expect(screen.queryByText('environments.yaml:0')).not.toBeInTheDocument()
  })
})
