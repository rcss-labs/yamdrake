import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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

  it('renders a group row as one cell spanning every column, and marks a selected row', () => {
    render(
      <DataTable
        label="Jobs"
        columns={columns}
        rows={[
          { id: 'g1', name: 'Payments' },
          { id: 'r1', name: 'deploy-orders' },
        ]}
        rowKey={(r) => r.id}
        rowKind={(r) => (r.id === 'g1' ? 'group' : 'selected')}
        empty={null}
      />,
    )
    const groupCell = screen.getByRole('cell', { name: 'g1' })
    expect(groupCell).toHaveAttribute('colspan', '2')
    expect(groupCell.closest('tr')).toHaveClass('ui-row-group')
    expect(screen.getByRole('row', { name: /deploy-orders/ })).toHaveClass('ui-row-selected')
  })

  it('shows the empty node when there are no rows', () => {
    render(<DataTable label="Runs" columns={columns} rows={[]} rowKey={(r: Row) => r.id} empty={<p>No runs yet</p>} />)
    expect(screen.getByText('No runs yet')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })
})

// jsdom does no layout, so this guards the cause of the sideways page scroll found at 380px: the visually hidden
// header text is position:absolute and escaped the scroll box. Checked in Chrome at 360px and 380px on /cli and
// /admin/groups (document scrollWidth equals clientWidth).
describe('DataTable scroll box', () => {
  it('is a positioning context so absolutely positioned header text stays inside it', () => {
    const rules = readFileSync(resolve(__dirname, 'DataTable.css'), 'utf8')
    const scrollRule = /\.ui-table-scroll\s*\{([^}]*)\}/.exec(rules)?.[1] ?? ''
    expect(scrollRule).toMatch(/position:\s*relative/)
  })
})
