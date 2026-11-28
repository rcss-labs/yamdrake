import { Navigate, useNavigate, useParams } from 'react-router-dom'
import { canSomewhere } from '../../auth/permissions'
import { useMe } from '../../auth/session'
import { NoAccess } from '../../shell/NoAccess'
import { NotFound } from '../../shell/NotFound'
import { PageHeader, Tabs } from '../../ui'
import { adminSections } from './sections'

const PANEL_ID = 'admin-section-panel'

export function AdminPage() {
  const me = useMe()
  const navigate = useNavigate()
  const { section: slug } = useParams()

  const visible = adminSections.filter((section) => canSomewhere(me, section.action))
  const first = visible[0]
  if (!first) {
    return <NoAccess />
  }
  if (slug === undefined) {
    return <Navigate replace to={`/admin/${first.slug}`} />
  }
  const requested = adminSections.find((section) => section.slug === slug)
  if (!requested) {
    return <NotFound />
  }
  if (!visible.includes(requested)) {
    return <NoAccess />
  }

  const Section = requested.component
  return (
    <>
      <PageHeader
        title="Admin"
        lead="Users, roles and connections are managed here and stored encrypted in the database, not in YAML"
      />
      <Tabs
        label="Admin sections"
        tabs={visible.map(({ slug: id, label }) => ({ id, label }))}
        active={requested.slug}
        onChange={(id) => navigate(`/admin/${id}`)}
        panelId={() => PANEL_ID}
      />
      <div role="tabpanel" id={PANEL_ID} aria-label={requested.label}>
        <Section />
      </div>
    </>
  )
}
