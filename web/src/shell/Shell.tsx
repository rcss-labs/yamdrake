import { ErrorBoundary } from './ErrorBoundary'
import { Sidebar } from './Sidebar'
import './shell.css'
import { AppRoutes } from '../routes'

export function Shell() {
  return (
    <ErrorBoundary>
      <div className="shell">
        <Sidebar />
        <main className="shell-main">
          <div className="shell-page">
            <AppRoutes />
          </div>
        </main>
      </div>
    </ErrorBoundary>
  )
}
