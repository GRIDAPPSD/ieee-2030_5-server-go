// Client for the management-pair endpoints (internal/handler/admin_management.go).
// The server lists pairs only per manager or per managed device, never all
// at once, so the panel's list is one lookup per registered device.

import { deleteJSON, fetchJSON, postJSON, type ApiResult } from './api'
import { canon, deviceKind } from './devices'

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

// pickableDevices is who an aggregator's device picker offers: not the
// aggregator itself, not another aggregator (stored kind, or a manager in
// pairs), and not a device another manager already holds. A device this
// manager already holds stays offered, so an edit can untick it.
export function pickableDevices<T extends { lfdi: string }>(
  devices: T[],
  pairs: ManagementPair[],
  manager?: string,
): T[] {
  const self = manager ? canon(manager) : ''
  const managers = new Set(pairs.map((p) => p.managerLFDI))
  const heldBy = new Map(pairs.map((p) => [p.managedLFDI, p.managerLFDI]))
  return devices.filter((d) => {
    const lfdi = canon(d.lfdi)
    if (lfdi === self) return false
    if (deviceKind(lfdi) === 'aggregator' || managers.has(lfdi)) return false
    const holder = heldBy.get(lfdi)
    return holder === undefined || holder === self
  })
}

export interface PairOutcome {
  lfdi: string
  action: 'add' | 'remove'
  result: ApiResult<unknown>
}

// applyPairs removes then adds one aggregator's pairs, one call each, and
// returns every outcome. A failed call does not stop the rest, so one bad
// device leaves the pairs that worked in place.
export async function applyPairs(manager: string, add: string[], remove: string[]): Promise<PairOutcome[]> {
  const outcomes: PairOutcome[] = []
  for (const lfdi of remove) outcomes.push({ lfdi, action: 'remove', result: await removePair(lfdi) })
  for (const lfdi of add) outcomes.push({ lfdi, action: 'add', result: await createPair(manager, lfdi) })
  return outcomes
}

// pairFailures names each failed outcome by its device, so the operator sees
// which pair the server refused and why.
export function pairFailures(outcomes: PairOutcome[], label: (lfdi: string) => string): string[] {
  return outcomes.flatMap((o) =>
    o.result.ok ? [] : [`${label(o.lfdi)}: ${o.result.error} (${o.result.status})`],
  )
}

// pairFailureText is the one sentence every form shows for refused pairs.
export function pairFailureText(failed: string[]): string {
  return `Pair failed for ${failed.join('; ')}`
}
