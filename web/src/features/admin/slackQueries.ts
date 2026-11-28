import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import { useApiMutation } from '../../api/hooks'
import type { SlackSettings, SlackTestResult } from '../../api/types'
import { slackKey } from '../runs/queries'
import { useMutation } from '@tanstack/react-query'

/** The token is a secret: gcTime 0 keeps it out of the mutation cache once the request is done. */
export function useSaveSlackToken() {
  return useApiMutation<SlackSettings, { token: string }>('PUT', '/api/v1/admin/slack', { invalidate: [slackKey], gcTime: 0 })
}

export function useTestSlack() {
  return useMutation<SlackTestResult, ApiError, { channel: string }>({
    mutationFn: (body) => api<SlackTestResult>('POST', '/api/v1/admin/slack/test', body),
  })
}

/** Slack's and Yumdrake's answer codes in plain words. */
const testFailureWords: Record<string, string> = {
  channel_not_found: 'Slack could not find that channel',
  not_in_channel: 'The bot is not in that channel. Invite it there first',
  is_archived: 'That channel is archived',
  invalid_auth: 'Slack does not accept this token',
  token_revoked: 'This token was revoked in Slack',
  account_inactive: 'This Slack account is inactive',
  missing_scope: 'The token lacks the permission to post messages',
  not_configured: 'Set a token first',
}

export function testFailureMessage(code: string): string {
  return testFailureWords[code] ?? `Slack refused the message (${code || 'unknown reason'})`
}
