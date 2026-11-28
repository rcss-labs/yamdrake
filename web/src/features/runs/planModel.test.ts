import { describe, expect, it } from 'vitest'
import { plansFixture, runIds } from '../../test/fixtures'
import { failedChecks, fallbackItems } from './planModel'

describe('failedChecks', () => {
  it('returns only the checks that failed', () => {
    const plan = plansFixture[runIds.plannedFailedCheck]!
    expect(failedChecks(plan)).toEqual([expect.objectContaining({ name: 'image-exists', service: 'billing', status: 'failed' })])
  })

  it('is empty when every check passed or could not be verified, and before the plan has loaded', () => {
    expect(failedChecks(plansFixture[runIds.planned])).toEqual([])
    expect(failedChecks(undefined)).toEqual([])
  })
})

describe('fallbackItems', () => {
  it('finds the config files that came from the dev branch', () => {
    const found = fallbackItems(plansFixture[runIds.planned])
    expect(found.map(({ service, item }) => `${service.name}/${item.name}`)).toEqual(['backend-api/backend-api.env'])
    expect(fallbackItems(plansFixture[runIds.plannedFailedCheck])).toEqual([])
  })
})
