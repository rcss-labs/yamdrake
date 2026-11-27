import type { EnvironmentSummary, Inventory } from '../../api/types'
import { Card, DataTable, Tag } from '../../ui'
import type { DataColumn } from '../../ui'

export function EnvironmentsTab({ inventory }: { inventory: Inventory }) {
  const serviceCount = (environment: string) =>
    inventory.services.filter((service) => service.environments.includes(environment)).length

  const columns: DataColumn<EnvironmentSummary>[] = [
    { key: 'name', header: 'Environment', render: (row) => <span className="mono inv-strong">{row.name}</span> },
    { key: 'label', header: 'Label', render: (row) => <Tag>{row.label}</Tag> },
    { key: 'account', header: 'AWS account', render: (row) => <span className="mono">{row.aws_account}</span> },
    { key: 'services', header: 'Services set up', className: 'num', render: (row) => serviceCount(row.name) },
  ]

  return (
    <Card flush>
      <DataTable
        label="Environments"
        columns={columns}
        rows={inventory.environments}
        rowKey={(row) => row.name}
        minWidth={460}
        empty={<p className="inv-empty">No environments are defined in this commit.</p>}
      />
    </Card>
  )
}
