import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import { useApiGet } from '../../api/hooks'
import type { SsoSettings, SsoTestResult, UpdateSsoRequest } from '../../api/types'

const ssoKey = ['admin', 'sso']

export const useSsoSettings = () => useApiGet<SsoSettings>(ssoKey, '/api/v1/admin/sso')

export function useSaveSso() {
  const queryClient = useQueryClient()
  return useMutation<SsoSettings, ApiError, UpdateSsoRequest>({
    mutationFn: (body) => api<SsoSettings>('PUT', '/api/v1/admin/sso', body),
    onSuccess: (saved) => queryClient.setQueryData(ssoKey, saved),
  })
}

export function useTestSso() {
  return useMutation<SsoTestResult, ApiError, void>({
    mutationFn: () => api<SsoTestResult>('POST', '/api/v1/admin/sso/test'),
  })
}
