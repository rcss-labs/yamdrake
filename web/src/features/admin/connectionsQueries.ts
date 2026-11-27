import { useMutation } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ApiError } from '../../api/error'
import { useApiGet, useApiMutation } from '../../api/hooks'
import type {
  AwsAccount,
  AwsAccountsResponse,
  AwsAccountTestResult,
  CreateAwsAccountRequest,
  CreateGithubConnectionRequest,
  GithubConnection,
  GithubConnectionsResponse,
  Inventory,
  InventorySource,
  InventorySourceRequest,
  JobsResponse,
} from '../../api/types'

const awsKey = ['admin', 'aws-accounts']
const githubKey = ['admin', 'github-connections']
const inventorySourceKey = ['admin', 'inventory-source']

export function useAwsAccounts() {
  return useApiGet<AwsAccountsResponse>(awsKey, '/api/v1/admin/aws-accounts')
}

export function useCreateAwsAccount() {
  return useApiMutation<AwsAccount, CreateAwsAccountRequest>('POST', '/api/v1/admin/aws-accounts', { invalidate: [awsKey], gcTime: 0 })
}

/** Tests one account; the answer is shown in its row, so nothing is cached. */
export function useTestAwsAccount() {
  return useMutation<AwsAccountTestResult, ApiError, number>({
    mutationFn: (id) => api<AwsAccountTestResult>('POST', `/api/v1/admin/aws-accounts/${id}/test`),
  })
}

export function useGithubConnections() {
  return useApiGet<GithubConnectionsResponse>(githubKey, '/api/v1/admin/github-connections')
}

export function useCreateGithubConnection() {
  return useApiMutation<GithubConnection, CreateGithubConnectionRequest>('POST', '/api/v1/admin/github-connections', {
    invalidate: [githubKey],
    gcTime: 0,
  })
}

export function useInventory() {
  return useApiGet<Inventory>(['inventory'], '/api/v1/inventory')
}

/** The stored inventory source; empty strings when none is stored. */
export function useInventorySource() {
  return useApiGet<InventorySource>(inventorySourceKey, '/api/v1/admin/inventory-source')
}

/** Replaces the inventory source and reads it again; the answer is the new inventory state. */
export function useSaveInventorySource() {
  return useApiMutation<Inventory, InventorySourceRequest>('PUT', '/api/v1/admin/inventory-source', {
    invalidate: [['inventory'], inventorySourceKey],
  })
}

export function useJobSources() {
  return useApiGet<JobsResponse>(['jobs'], '/api/v1/jobs')
}

/**
 * The server sends one plain message for a rejected form, not one per field. This picks the field the
 * message is about by finding its API name in the text ("account_id must be 12 digits"); a message that
 * names none of the fields, or several of them, belongs to the whole form.
 */
export function fieldForMessage<Name extends string>(message: string, fieldNames: readonly Name[]): Name | undefined {
  const lower = message.toLowerCase()
  const named = fieldNames.filter((name) => new RegExp(`(^|[^a-z_])${name}([^a-z_]|$)`).test(lower))
  return named.length === 1 ? named[0] : undefined
}
