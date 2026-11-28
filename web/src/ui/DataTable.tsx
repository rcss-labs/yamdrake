import type { ComponentPropsWithoutRef, Key, ReactNode } from 'react'
import { cx } from './classnames'
import './DataTable.css'

export interface DataColumn<Row> {
  key: string
  header: ReactNode
  render: (row: Row) => ReactNode
  className?: string
}

export interface DataTableProps<Row> extends Omit<ComponentPropsWithoutRef<'div'>, 'children'> {
  columns: DataColumn<Row>[]
  rows: Row[]
  rowKey: (row: Row) => Key
  // Accessible name of the table.
  label: string
  // Shown instead of the table when there are no rows.
  empty: ReactNode
  minWidth?: number | string
  // "group" renders the row as one heading cell spanning every column (using the
  // first column's render); "selected" highlights it.
  rowKind?: (row: Row) => 'group' | 'selected' | undefined
}

export function DataTable<Row>({ columns, rows, rowKey, label, empty, minWidth, rowKind, className, ...rest }: DataTableProps<Row>) {
  return (
    <div className={cx('ui-table-scroll', className)} {...rest}>
      {rows.length === 0 ? (
        empty
      ) : (
        <table className="ui-table" aria-label={label} style={minWidth === undefined ? undefined : { minWidth }}>
          <thead>
            <tr>
              {columns.map((column) => (
                <th key={column.key} scope="col" className={column.className}>
                  {column.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const kind = rowKind?.(row)
              const first = columns[0]
              if (kind === 'group' && first) {
                return (
                  <tr key={rowKey(row)} className="ui-row-group">
                    <td colSpan={columns.length}>{first.render(row)}</td>
                  </tr>
                )
              }
              return (
                <tr key={rowKey(row)} className={kind === 'selected' ? 'ui-row-selected' : undefined}>
                  {columns.map((column) => (
                    <td key={column.key} className={column.className}>
                      {column.render(row)}
                    </td>
                  ))}
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </div>
  )
}
