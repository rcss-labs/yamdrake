import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, onUnauthorized } from './client'
import { delayNext, failNext, requests, server } from '../test/server'

describe('api', () => {
  it('returns the parsed JSON body', async () => {
    const folders = await api<{ folders: { name: string }[] }>('GET', '/api/v1/folders')
    expect(folders.folders.map((folder) => folder.name)).toContain('Backend')
  })

  it('sends a JSON body with a JSON content type', async () => {
    await api('POST', '/api/v1/folders', { name: 'Tools' })
    const sent = requests.find((request) => request.method === 'POST' && request.path === '/api/v1/folders')
    expect(sent?.body).toEqual({ name: 'Tools' })
  })

  it('returns undefined for 204 No Content', async () => {
    await expect(api('POST', '/api/v1/auth/logout')).resolves.toBeUndefined()
  })

  it('parses the error body into an ApiError', async () => {
    failNext('GET', '/api/v1/folders', 403, 'forbidden', 'You may not see folders')
    const error = await api('GET', '/api/v1/folders').catch((caught: unknown) => caught)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 403, code: 'forbidden', message: 'You may not see folders' })
  })

  it('falls back to http_<status> when the error body is not the API format', async () => {
    server.use(http.get('/api/v1/folders', () => new HttpResponse('<html>Bad gateway</html>', { status: 502 })))
    const error = await api('GET', '/api/v1/folders').catch((caught: unknown) => caught)
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).status).toBe(502)
    expect((error as ApiError).code).toBe('http_502')
    expect((error as ApiError).message).not.toBe('')
  })

  it('maps a network failure to the code "network"', async () => {
    server.use(http.get('/api/v1/folders', () => HttpResponse.error()))
    const error = await api('GET', '/api/v1/folders').catch((caught: unknown) => caught)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 0, code: 'network' })
  })

  it('exposes Retry-After in seconds on the error', async () => {
    failNext('POST', '/api/v1/auth/login', 429, 'rate_limited', 'Too many attempts', { 'Retry-After': '600' })
    const error = await api('POST', '/api/v1/auth/login', { email: 'a', password: 'b' }).catch((caught: unknown) => caught)
    expect((error as ApiError).retryAfterSeconds).toBe(600)
  })

  describe('with fake timers', () => {
    afterEach(() => {
      vi.useRealTimers()
    })

    it('gives up after 15 seconds with the code "network"', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      delayNext('GET', '/api/v1/folders', 60_000)
      const outcome = api('GET', '/api/v1/folders').catch((caught: unknown) => caught)
      await vi.advanceTimersByTimeAsync(14_999)
      let settled = false
      void outcome.then(() => {
        settled = true
      })
      await vi.advanceTimersByTimeAsync(0)
      expect(settled).toBe(false)
      await vi.advanceTimersByTimeAsync(2)
      const error = await outcome
      expect(error).toBeInstanceOf(ApiError)
      expect(error).toMatchObject({ code: 'network' })
      expect((error as ApiError).message).toMatch(/in time/i)
    })
  })

  describe('401 notifications', () => {
    it('tells subscribers when a request is answered with 401', async () => {
      const listener = vi.fn()
      const unsubscribe = onUnauthorized(listener)
      failNext('GET', '/api/v1/folders', 401, 'unauthorized', 'Sign in')
      await api('GET', '/api/v1/folders').catch(() => undefined)
      unsubscribe()
      expect(listener).toHaveBeenCalledTimes(1)
    })

    it('does not notify for the sign-in routes, where 401 means wrong credentials', async () => {
      const listener = vi.fn()
      const unsubscribe = onUnauthorized(listener)
      failNext('POST', '/api/v1/auth/login', 401, 'unauthorized', 'Wrong')
      await api('POST', '/api/v1/auth/login', { email: 'a', password: 'b' }).catch(() => undefined)
      unsubscribe()
      expect(listener).not.toHaveBeenCalled()
    })

    it('stops notifying after unsubscribe', async () => {
      const listener = vi.fn()
      onUnauthorized(listener)()
      failNext('GET', '/api/v1/folders', 401, 'unauthorized', 'Sign in')
      await api('GET', '/api/v1/folders').catch(() => undefined)
      expect(listener).not.toHaveBeenCalled()
    })
  })
})
