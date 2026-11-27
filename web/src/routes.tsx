import { Route, Routes } from 'react-router-dom'
import { AdminPage } from './features/admin/AdminPage'
import { InventoryPage } from './features/inventory/InventoryPage'
import { JobPage } from './features/jobs/JobPage'
import { JobsPage } from './features/jobs/JobsPage'
import { ConfirmDevicePage } from './features/tokens/ConfirmDevicePage'
import { TokensPage } from './features/tokens/TokensPage'
import { NotFound } from './shell/NotFound'
import { Card, PageHeader } from './ui'

function OverviewPage() {
  return (
    <>
      <PageHeader title="Overview" />
      <Card>The overview arrives with run data.</Card>
    </>
  )
}

function ComingPage({ title }: { title: string }) {
  return (
    <>
      <PageHeader title={title} />
      <Card>{title} arrive with run data.</Card>
    </>
  )
}

/** The whole route table. The sign-in address is handled by the session provider, not here. */
export function AppRoutes() {
  return (
    <Routes>
      <Route path="/" element={<OverviewPage />} />
      <Route path="/jobs" element={<JobsPage />} />
      <Route path="/jobs/:id" element={<JobPage />} />
      <Route path="/runs" element={<ComingPage title="Runs" />} />
      <Route path="/approvals" element={<ComingPage title="Approvals" />} />
      <Route path="/versions" element={<ComingPage title="Versions" />} />
      <Route path="/inventory" element={<InventoryPage />} />
      <Route path="/cli" element={<TokensPage />} />
      <Route path="/cli/confirm" element={<ConfirmDevicePage />} />
      <Route path="/admin/:section?" element={<AdminPage />} />
      <Route path="*" element={<NotFound />} />
    </Routes>
  )
}
