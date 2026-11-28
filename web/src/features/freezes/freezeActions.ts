import { useApiMutation } from '../../api/hooks'
import type { CreateFreezeRequest, Freeze } from '../../api/types'
import { freezesKey } from '../runs/queries'

export function useCreateFreeze() {
  return useApiMutation<Freeze, CreateFreezeRequest>('POST', '/api/v1/admin/freezes', { invalidate: [freezesKey] })
}

export function useLiftFreeze() {
  return useApiMutation<void, { id: number }>('DELETE', ({ id }) => `/api/v1/admin/freezes/${id}`, { invalidate: [freezesKey] })
}
