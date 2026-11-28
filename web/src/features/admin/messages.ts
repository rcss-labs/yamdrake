import { errorMessage } from '../../api/client'
import { ApiError } from '../../api/error'

export const lastAdminMessage = 'At least one enabled admin must remain'

/** True when the server says the refusal is about the last remaining enabled admin, by its code or its message. */
function isLastAdminRefusal(error: ApiError): boolean {
  return /last[_ ]admin/i.test(error.code) || /at least one.*admin/i.test(error.message)
}

/** A 409 on a change to a user or group: the server's own words, except for the last-admin rule, which has one wording. */
export function changeConflictMessage(error: unknown): string {
  if (error instanceof ApiError && error.status === 409 && isLastAdminRefusal(error)) {
    return lastAdminMessage
  }
  return errorMessage(error)
}
