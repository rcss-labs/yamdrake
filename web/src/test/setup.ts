import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { resetMockApi } from './handlers'
import { server } from './server'

// Node's fetch needs absolute URLs; the app calls the API with same-origin paths like a browser does.
const nodeFetch = globalThis.fetch.bind(globalThis)
globalThis.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
  if (typeof input === 'string' && input.startsWith('/')) {
    return nodeFetch(new URL(input, window.location.origin), init)
  }
  return nodeFetch(input, init)
}

beforeAll(() => server.listen({ onUnhandledFrame: 'error' }))
afterEach(() => {
  cleanup()
  server.resetHandlers()
  resetMockApi()
})
afterAll(() => server.close())
