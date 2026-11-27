import { useApiGet, useApiMutation } from '../../api/hooks'
import type { ApiToken, CreatedToken, CreateTokenRequest, DeviceLoginConfirmRequest, TokensResponse } from '../../api/types'

export function useTokens() {
  return useApiGet<TokensResponse>(['tokens'], '/api/v1/tokens')
}

export function useCreateToken() {
  return useApiMutation<CreatedToken, CreateTokenRequest>('POST', '/api/v1/tokens', {
    invalidate: [['tokens']],
    // The result holds the one-time plaintext: drop it from the mutation cache as soon as nothing observes it.
    gcTime: 0,
  })
}

export function useRevokeToken() {
  return useApiMutation<void, ApiToken>('DELETE', (token) => `/api/v1/tokens/${token.id}`, { invalidate: [['tokens']] })
}

export function useConfirmDevice() {
  return useApiMutation<void, DeviceLoginConfirmRequest>('POST', '/api/v1/cli/login/confirm')
}
