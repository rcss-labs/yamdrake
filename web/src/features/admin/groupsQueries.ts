import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import { useApiGet } from '../../api/hooks'
import type {
  AdminGroup,
  AdminGrant,
  CreateGrantRequest,
  CreateGroupRequest,
  FoldersResponse,
  GrantsResponse,
  GroupsResponse,
  IdpMapping,
  IdpMappingsResponse,
  Inventory,
  PutIdpMappingRequest,
  UpdateGroupRequest,
} from '../../api/types'

const groupsKey = ['admin', 'groups']
const grantsKey = ['admin', 'grants']
const mappingsKey = ['admin', 'idp-mappings']

export const useGroups = () => useApiGet<GroupsResponse>(groupsKey, '/api/v1/admin/groups')
export const useGrants = () => useApiGet<GrantsResponse>(grantsKey, '/api/v1/admin/grants')
export const useMappings = () => useApiGet<IdpMappingsResponse>(mappingsKey, '/api/v1/admin/idp-mappings')
// Only used to fill selects and name folders; the section still works when they fail.
export const useFolderChoices = () => useApiGet<FoldersResponse>(['folders'], '/api/v1/folders')
export const useInventoryChoices = () => useApiGet<Inventory>(['inventory'], '/api/v1/inventory')

// Several of these send a body that is not the whole argument (the id goes in the path), so each calls api() itself.
export function useCreateGroup() {
  const queryClient = useQueryClient()
  return useMutation<AdminGroup, ApiError, CreateGroupRequest>({
    mutationFn: (body) => api<AdminGroup>('POST', '/api/v1/admin/groups', body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: groupsKey }),
  })
}

export function useSetGroupAdmin() {
  const queryClient = useQueryClient()
  return useMutation<AdminGroup, ApiError, { id: number } & UpdateGroupRequest>({
    mutationFn: ({ id, admin }) => api<AdminGroup>('PATCH', `/api/v1/admin/groups/${id}`, { admin }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: groupsKey }),
  })
}

export function useAddGrant() {
  const queryClient = useQueryClient()
  return useMutation<AdminGrant, ApiError, CreateGrantRequest>({
    mutationFn: (body) => api<AdminGrant>('POST', '/api/v1/admin/grants', body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: grantsKey }),
  })
}

export function useRemoveGrant() {
  const queryClient = useQueryClient()
  return useMutation<void, ApiError, number>({
    mutationFn: (id) => api<void>('DELETE', `/api/v1/admin/grants/${id}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: grantsKey }),
  })
}

export function usePutMapping() {
  const queryClient = useQueryClient()
  return useMutation<IdpMapping, ApiError, { idpGroup: string } & PutIdpMappingRequest>({
    mutationFn: ({ idpGroup, group_id }) =>
      api<IdpMapping>('PUT', `/api/v1/admin/idp-mappings/${encodeURIComponent(idpGroup)}`, { group_id }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: mappingsKey }),
  })
}

export function useRemoveMapping() {
  const queryClient = useQueryClient()
  return useMutation<void, ApiError, string>({
    mutationFn: (idpGroup) => api<void>('DELETE', `/api/v1/admin/idp-mappings/${encodeURIComponent(idpGroup)}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: mappingsKey }),
  })
}
