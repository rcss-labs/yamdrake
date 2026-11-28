import { Link } from 'react-router-dom'
import { Card, PageHeader } from '../ui'

export function NotFound() {
  return (
    <>
      <PageHeader title="Page not found" lead="There is nothing at this address." />
      <Card>
        <Link to="/jobs">Go to Jobs</Link>
      </Card>
    </>
  )
}
