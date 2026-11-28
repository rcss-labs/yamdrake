import { setupServer } from 'msw/node'
import { handlers } from './handlers'

/** The MSW server over the complete mock API. Add per-test overrides with server.use(...). */
export const server = setupServer(...handlers)

export { delayNext, failNext, mockState, requests, resetMockApi, signInAs, signOut } from './handlers'
export type { SeenRequest } from './handlers'
export * from './fixtures'
export { eventSources, FakeEventSource } from './eventSource'
