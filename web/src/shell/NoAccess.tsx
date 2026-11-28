import { Link } from 'react-router-dom'
import { Card, PageHeader } from '../ui'

export function NoAccess() {
  return (
    <>
      <PageHeader title="You do not have access" lead="Your account is not allowed to open this page." />
      <Card>
        Ask an administrator to grant you access, or <Link to="/jobs">go to Jobs</Link>.
      </Card>
    </>
  )
}
