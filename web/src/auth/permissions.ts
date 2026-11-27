import type { Action, Me } from '../api/types'

/** What an action is about. A grant that sets a scope only matches a target with the same value. */
export interface Target {
  label?: string
  environment?: string
  folderId?: number
}

/**
 * The same rule as the server (Plan 3, Task 2): an admin may do everything, everyone may view,
 * and anything else needs a grant with that action whose set scopes all equal the target's.
 */
export function can(me: Me, action: Action, target: Target = {}): boolean {
  if (me.admin || action === 'view') {
    return true
  }
  return me.grants.some(
    (grant) =>
      grant.action === action &&
      (grant.label === '' || grant.label === target.label) &&
      (grant.environment === '' || grant.environment === target.environment) &&
      (grant.folder_id === 0 || grant.folder_id === target.folderId),
  )
}

/** True if the person has the action anywhere; used to decide whether to show navigation. */
export function canSomewhere(me: Me, action: Action): boolean {
  return me.admin || action === 'view' || me.grants.some((grant) => grant.action === action)
}
