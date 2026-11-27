import { describe, expect, it } from 'vitest'
import { safeNext } from './next'

describe('safeNext', () => {
  it.each([
    ['/jobs', '/jobs'],
    ['/jobs/7?tab=definition', '/jobs/7?tab=definition'],
    ['/', '/'],
  ])('keeps the same-site path %s', (value, expected) => {
    expect(safeNext(value)).toBe(expected)
  })

  it.each([
    ['//evil.example'],
    ['//evil.example/jobs'],
    ['/\\evil.example'],
    ['\\\\evil.example'],
    ['https://evil.example'],
    ['http://evil.example/jobs'],
    ['javascript:alert(1)'],
    ['evil.example'],
    ['jobs'],
    ['/%2F%2Fevil.example'],
    ['/\t/evil.example'],
    ['/\n/evil.example'],
    [''],
  ])('replaces the unsafe value %j with /', (value) => {
    expect(safeNext(value)).toBe('/')
  })

  it('falls back to / when nothing is given', () => {
    expect(safeNext(null)).toBe('/')
    expect(safeNext(undefined)).toBe('/')
  })

  it('never returns the sign-in page itself', () => {
    expect(safeNext('/sign-in?next=/jobs')).toBe('/')
    expect(safeNext('/sign-in')).toBe('/')
  })
})
