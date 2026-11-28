import { useParams } from 'react-router-dom'
import { PageHeader } from '../../ui'

// Placeholder: Task 4 of Plan 8b fills this in (it also owns RunActions, RunFacts and WhatFailed).
export function RunPage() {
  const { id = '' } = useParams()
  return <PageHeader title={`Run #${id}`} />
}
