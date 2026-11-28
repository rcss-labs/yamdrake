import { PageHeader } from '../../ui'
import { RunsList } from './RunsList'

export function RunsPage() {
  return (
    <>
      <PageHeader title="Runs" lead="Every run of every job, started from the web or the CLI" />
      <RunsList search noRunsText="No runs yet" />
    </>
  )
}
