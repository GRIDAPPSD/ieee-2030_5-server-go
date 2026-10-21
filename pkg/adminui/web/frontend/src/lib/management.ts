// Client for the management-pair endpoints (internal/handler/admin_management.go).
// The server lists pairs only per manager or per managed device, never all
// at once, so the panel's list is one lookup per registered device.

import { deleteJSON, fetchJSON, postJSON, type ApiResult } from './api'
import { canon } from './devices'

export interface ManagementPair {
  managerLFDI: string
  managedLFDI: string
}

interface ManagedList {
  managerLFDI: string
  managedLFDIs: string[] | null
}

const PATH = '/api/management-pairs'

// pairsKey is stable across reorderings, so a device list that arrives in a
// new order with the same members does not trigger a reload.
export function pairsKey(lfdis: string[]): string {
  return lfdis.map(canon).sort().join(',')
}

export async function listPairs(lfdis: string[]): Promise<ApiResult<ManagementPair[]>> {
  const results = await Promise.all(
    lfdis.map((lfdi) => fetchJSON<ManagedList>(`${PATH}?manager=${encodeURIComponent(canon(lfdi))}`)),
  )
  const pairs: ManagementPair[] = []
  for (const res of results) {
    if (!res.ok) return res
    for (const managed of res.data.managedLFDIs ?? []) {
      pairs.push({ managerLFDI: canon(res.data.managerLFDI), managedLFDI: canon(managed) })
    }
  }
  return { ok: true, data: pairs }
}

export function createPair(managerLFDI: string, managedLFDI: string): Promise<ApiResult<ManagementPair>> {
  return postJSON<ManagementPair>(PATH, { managerLFDI: canon(managerLFDI), managedLFDI: canon(managedLFDI) })
}

export function removePair(managedLFDI: string): Promise<ApiResult<void>> {
  return deleteJSON<void>(`${PATH}?managed=${encodeURIComponent(canon(managedLFDI))}`)
}
