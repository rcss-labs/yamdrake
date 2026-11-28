import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { eventSources, installFakeEventSource } from './eventSource'
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

// jsdom has no EventSource: every test gets the controllable fake from ./eventSource.
installFakeEventSource()

// jsdom 29 has HTMLDialogElement but no show, showModal or close. This gives them the part of the browser's
// behaviour the screens rely on: the `open` attribute, and a `close` event when close() is called.
// (It does not make the rest of the page inert; ConfirmDialog traps focus itself.)
if (typeof HTMLDialogElement !== 'undefined' && typeof HTMLDialogElement.prototype.showModal !== 'function') {
  HTMLDialogElement.prototype.show = function show(this: HTMLDialogElement) {
    this.setAttribute('open', '')
  }
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.setAttribute('open', '')
  }
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    if (this.hasAttribute('open')) {
      this.removeAttribute('open')
      this.dispatchEvent(new Event('close'))
    }
  }
}

beforeAll(() => server.listen({ onUnhandledFrame: 'error' }))
afterEach(() => {
  cleanup()
  server.resetHandlers()
  resetMockApi()
  eventSources.reset()
})
afterAll(() => server.close())
