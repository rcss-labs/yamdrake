import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { DataTable } from './DataTable'

interface Row {
  id: string
  name: string
}
const columns = [
  { key: 'id', header: 'Run', render: (r: Row) => r.id },
  { key: 'name', header: 'Job', render: (r: Row) => r.name },
]

describe('DataTable', () => {
  it('renders headers and a row per item', () => {
    render(
      <DataTable
        label="Runs"
        columns={columns}
        rows={[
          { id: 'r1', name: 'deploy-orders' },
          { id: 'r2', name: 'deploy-billing' },
        ]}
        rowKey={(r) => r.id}
        empty={<p>No runs</p>}
      />,
    )
    expect(screen.getByRole('table', { name: 'Runs' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Job' })).toBeInTheDocument()
    expect(within(screen.getByRole('row', { name: /deploy-billing/ })).getByText('deploy-billing')).toBeInTheDocument()
    expect(screen.queryByText('No runs')).not.toBeInTheDocument()
  })

  it('shows the empty node when there are no rows', () => {
    render(<DataTable label="Runs" columns={columns} rows={[]} rowKey={(r: Row) => r.id} empty={<p>No runs yet</p>} />)
    expect(screen.getByText('No runs yet')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })
})
