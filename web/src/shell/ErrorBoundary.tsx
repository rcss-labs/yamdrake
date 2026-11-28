import { Component } from 'react'
import type { ErrorInfo, ReactNode } from 'react'
import { Button } from '../ui'
import './shell.css'

interface ErrorBoundaryState {
  failed: boolean
}

/**
 * Catches a screen that throws while rendering, so a bug or an unexpected answer shows a readable message
 * and a way out instead of a blank page.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: false }

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { failed: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('The page failed to render', error, info.componentStack)
  }

  render() {
    if (!this.state.failed) {
      return this.props.children
    }
    return (
      <div className="shell-crash" role="alert">
        <h1>Something went wrong</h1>
        <p>This page could not be shown. Reloading usually fixes it. If it keeps happening, tell an administrator.</p>
        <Button onClick={() => window.location.reload()}>Reload the page</Button>
      </div>
    )
  }
}
