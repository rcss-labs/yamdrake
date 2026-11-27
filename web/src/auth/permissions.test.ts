import { describe, expect, it } from 'vitest'
import { ALL_ACTIONS } from '../api/types'
import type { Action, Grant, Me } from '../api/types'
import { can, canSomewhere } from './permissions'

function person(overrides: Partial<Me> = {}): Me {
  return { id: 1, email: 'someone@example.com', name: 'Someone', admin: false, grants: [], ...overrides }
}

function grant(action: Action, scope: Partial<Grant> = {}): Grant {
  return { action, label: '', environment: '', folder_id: 0, ...scope }
}

const devTarget = { label: 'dev', environment: 'hetzner-01', folderId: 3 }
const prodTarget = { label: 'prod', environment: 'prod-01', folderId: 3 }

// The same cases as Plan 3's TestCan (internal/auth/auth_test.go).
describe('can', () => {
  it('lets an admin do all ten actions on any target', () => {
    const admin = person({ admin: true })
    for (const action of ALL_ACTIONS) {
      expect(can(admin, action, prodTarget), action).toBe(true)
      expect(can(admin, action), action).toBe(true)
    }
  })

  it('lets a person without grants only view', () => {
    const nobody = person()
    for (const action of ALL_ACTIONS) {
      expect(can(nobody, action, devTarget), action).toBe(action === 'view')
    }
  })

  it('applies a label scope', () => {
    const me = person({ grants: [grant('run', { label: 'dev' })] })
    expect(can(me, 'run', devTarget)).toBe(true)
    expect(can(me, 'run', prodTarget)).toBe(false)
  })

  it('applies an environment scope', () => {
    const me = person({ grants: [grant('run', { environment: 'hetzner-02' })] })
    expect(can(me, 'run', devTarget)).toBe(false)
    expect(can(me, 'run', { ...devTarget, environment: 'hetzner-02' })).toBe(true)
  })

  it('requires every scope the grant sets to match', () => {
    const me = person({ grants: [grant('run', { label: 'dev', folder_id: 3 })] })
    expect(can(me, 'run', devTarget)).toBe(true)
    expect(can(me, 'run', { ...devTarget, folderId: 4 })).toBe(false)
  })

  it('lets an unscoped grant match any target', () => {
    const me = person({ grants: [grant('run')] })
    expect(can(me, 'run', devTarget)).toBe(true)
    expect(can(me, 'run', prodTarget)).toBe(true)
    expect(can(me, 'run')).toBe(true)
  })

  it('does not let run imply approve or cancel', () => {
    const me = person({ grants: [grant('run')] })
    expect(can(me, 'approve', devTarget)).toBe(false)
    expect(can(me, 'cancel', devTarget)).toBe(false)
  })

  it('refuses view_logs without a grant', () => {
    expect(can(person(), 'view_logs', devTarget)).toBe(false)
    expect(can(person({ grants: [grant('view_logs')] }), 'view_logs', devTarget)).toBe(true)
  })

  it('does not match a scoped grant when no target is given', () => {
    const me = person({ grants: [grant('run', { label: 'dev' })] })
    expect(can(me, 'run')).toBe(false)
  })
})

describe('canSomewhere', () => {
  it('is true for view for everyone', () => {
    expect(canSomewhere(person(), 'view')).toBe(true)
  })

  it('is true when any grant has the action, whatever its scope', () => {
    const me = person({ grants: [grant('run', { label: 'dev', folder_id: 9 })] })
    expect(canSomewhere(me, 'run')).toBe(true)
    expect(canSomewhere(me, 'approve')).toBe(false)
  })

  it('is true for every action for an admin', () => {
    const admin = person({ admin: true })
    for (const action of ALL_ACTIONS) {
      expect(canSomewhere(admin, action), action).toBe(true)
    }
  })
})
