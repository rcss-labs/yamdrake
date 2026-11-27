import { useParams } from 'react-router-dom'
import { Card, PageHeader } from '../../ui'

export function JobPage() {
  const { id } = useParams()
  return (
    <>
      <PageHeader title="Job" />
      <Card>Job {id} is coming. This screen has not been built yet.</Card>
    </>
  )
}
