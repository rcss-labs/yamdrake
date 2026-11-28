// A controllable stand-in for the browser's EventSource, installed for every test by setup.ts
// (jsdom has none). A test drives it the way the server and the network would:
//
//   const source = eventSources.last()        the most recently opened stream (undefined before any)
//   source.url                                 what the page asked for, for example "/api/v1/runs/104/events"
//   source.open()                              the connection is up
//   source.emit('state', body)                 a `state` event; body is JSON-encoded for you
//   source.emit('log', page, 'c5')             a `log` event with its cursor as the event id
//   source.emit('end', body)                   the server is done
//   source.drop()                              the connection broke and the browser is retrying (readyState CONNECTING)
//   source.fail()                              the browser gave up (readyState CLOSED)
//   source.closed                              true once the page called close()
//   eventSources.urls()                        every URL opened since the last reset
//
// Wrap emit/open/drop in act() (from @testing-library/react) when a component or hook is listening.

type Listener = (event: Event) => void

export class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2

  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSED = 2

  readonly url: string
  readonly withCredentials = false
  readyState = 0
  /** True once the page called close(). */
  closed = false

  onopen: Listener | null = null
  onerror: Listener | null = null
  onmessage: Listener | null = null

  private readonly listeners = new Map<string, Set<Listener>>()

  constructor(url: string | URL) {
    this.url = String(url)
    eventSources.all.push(this)
  }

  addEventListener(type: string, listener: Listener): void {
    const set = this.listeners.get(type) ?? new Set<Listener>()
    set.add(listener)
    this.listeners.set(type, set)
  }

  removeEventListener(type: string, listener: Listener): void {
    this.listeners.get(type)?.delete(listener)
  }

  close(): void {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }

  private dispatch(event: Event, handler: Listener | null): void {
    if (this.closed) return
    handler?.(event)
    this.listeners.get(event.type)?.forEach((listener) => listener(event))
  }

  /** The connection is up (also what the browser reports after it reconnected). */
  open(): void {
    this.readyState = FakeEventSource.OPEN
    this.dispatch(new Event('open'), this.onopen)
  }

  /** A named event with a JSON body; `id` becomes the event's lastEventId (the log cursor). */
  emit(type: string, body: unknown, id = ''): void {
    this.readyState = FakeEventSource.OPEN
    this.dispatch(new MessageEvent(type, { data: JSON.stringify(body), lastEventId: id }), null)
  }

  /** The connection broke and the browser is retrying: readyState is CONNECTING. */
  drop(): void {
    this.readyState = FakeEventSource.CONNECTING
    this.dispatch(new Event('error'), this.onerror)
  }

  /** The browser gave up (for example the server answered with an error): readyState is CLOSED. */
  fail(): void {
    this.readyState = FakeEventSource.CLOSED
    this.dispatch(new Event('error'), this.onerror)
  }
}

export const eventSources = {
  all: [] as FakeEventSource[],
  last: (): FakeEventSource | undefined => eventSources.all[eventSources.all.length - 1],
  urls: (): string[] => eventSources.all.map((source) => source.url),
  reset: (): void => {
    eventSources.all.length = 0
  },
}

/** Replaces the global EventSource with the fake. setup.ts does this once for the whole test run. */
export function installFakeEventSource(): void {
  Object.defineProperty(globalThis, 'EventSource', { value: FakeEventSource, writable: true, configurable: true })
}
