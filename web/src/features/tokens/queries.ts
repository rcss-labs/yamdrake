import { useApiGet, useApiMutation } from '../../api/hooks'
import type { ApiToken, CreatedToken, CreateTokenRequest, DeviceLoginConfirmRequest, TokensResponse } from '../../api/types'

export function useTokens() {
  return useApiGet<TokensResponse>(['tokens'], '/api/v1/tokens')
}

export function useCreateToken() {
  return useApiMutation<CreatedToken, CreateTokenRequest>('POST', '/api/v1/tokens', { invalidate: [['tokens']] })
}

export function useRevokeToken() {
  return useApiMutation<void, ApiToken>('DELETE', (token) => `/api/v1/tokens/${token.id}`, { invalidate: [['tokens']] })
}

export function useConfirmDevice() {
  return useApiMutation<void, DeviceLoginConfirmRequest>('POST', '/api/v1/cli/login/confirm')
}
