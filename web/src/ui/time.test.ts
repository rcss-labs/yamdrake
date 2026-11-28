import { describe, expect, it } from 'vitest'
import { exactTime, formatDuration, relativeTime } from './time'

const now = Date.parse('2026-11-27T12:00:00Z')

describe('relativeTime', () => {
  it.each([
    ['2026-11-27T11:59:50Z', 'just now'],
    ['2026-11-27T11:59:00Z', '1 minute ago'],
    ['2026-11-27T11:57:00Z', '3 minutes ago'],
    ['2026-11-27T10:00:00Z', '2 hours ago'],
    ['2026-11-22T12:00:00Z', '5 days ago'],
    ['2026-11-27T12:03:00Z', 'in 3 minutes'],
    ['2026-11-27T16:00:00Z', 'in 4 hours'],
  ])('says %s as "%s"', (iso, words) => {
    expect(relativeTime(iso, now)).toBe(words)
  })
})

describe('formatDuration', () => {
  it.each([
    [0, '0s'],
    [45_000, '45s'],
    [134_000, '2m 14s'],
    [120_000, '2m 0s'],
    [3_725_000, '1h 2m'],
    [-5, '0s'],
  ])('writes %d ms as "%s"', (ms, words) => {
    expect(formatDuration(ms)).toBe(words)
  })
})

describe('exactTime', () => {
  it('writes the full time in the zone asked for', () => {
    expect(exactTime('2026-11-27T12:00:00Z', 'UTC')).toBe('27 Nov 2026, 12:00:00 UTC')
  })
})
