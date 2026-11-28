import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import { useApiGet, useApiMutation } from '../../api/hooks'
import type { CreatedToken, CreateTokenRequest, DeviceLoginConfirmRequest, TokensResponse } from '../../api/types'

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

/** DELETE one token by id; the id is only in the path, no body is sent. */
export function useRevokeToken() {
  const queryClient = useQueryClient()
  return useMutation<void, ApiError, number>({
    mutationFn: (id) => api<void>('DELETE', `/api/v1/tokens/${id}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tokens'] }),
  })
}

export function useConfirmDevice() {
  return useApiMutation<void, DeviceLoginConfirmRequest>('POST', '/api/v1/cli/login/confirm')
}
