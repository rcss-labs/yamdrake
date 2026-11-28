import { useApiGet, useApiMutation } from '../../api/hooks'
import type { Inventory, Service } from '../../api/types'

export function useInventory() {
  return useApiGet<Inventory>(['inventory'], '/api/v1/inventory')
}

export function useInventoryService(name: string) {
  return useApiGet<Service>(['inventory', 'services', name], `/api/v1/inventory/services/${encodeURIComponent(name)}`, {
    enabled: name !== '',
  })
}

// Invalidating ['inventory'] also refreshes every cached service.
export function useSyncInventory() {
  return useApiMutation<Inventory>('POST', '/api/v1/inventory/sync', { invalidate: [['inventory']] })
}
