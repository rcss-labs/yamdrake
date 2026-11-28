// Shared by the run page tests: render the page at /runs/:id with a stub for every other address a test lands on.
import { screen } from '@testing-library/react'
import { Route, Routes } from 'react-router-dom'
import type { Me, RunDetail, RunState } from '../../api/types'
import { renderApp } from '../../test/render'
import { mockState } from '../../test/server'
import { RunPage } from './RunPage'

export function renderRun(id: number, me?: Me) {
  return renderApp(
    <Routes>
      <Route path="/runs/:id" element={<RunPage />} />
      <Route path="/runs" element={<p>Runs list</p>} />
      <Route path="/jobs" element={<p>Jobs list</p>} />
      <Route path="/jobs/:id" element={<p>Job page</p>} />
    </Routes>,
    { route: `/runs/${id}`, me },
  )
}

/** The run in the mock server's state, to change before rendering. */
export function storedRun(id: number): RunDetail {
  const run = mockState().runs.find((candidate) => candidate.id === id)
  if (!run) throw new Error(`no run ${id} in the mock state`)
  return run
}

export function moveStoredRun(id: number, state: RunState, extra: Partial<RunDetail> = {}): RunDetail {
  const run = storedRun(id)
  Object.assign(run, { state }, extra)
  return run
}

/** The names of the buttons in the run's action bar, in order. */
export async function actionNames(): Promise<string[]> {
  const bar = await screen.findByRole('group', { name: 'Run actions' })
  return Array.from(bar.querySelectorAll('button')).map((button) => button.textContent ?? '')
}
