// Wire shapes for the admin FSA and topology endpoints
// (internal/handler/admin_fsa.go, internal/handler/admin_topology.go).
// The list endpoint wraps its array in an object, and the topology
// endpoint omits empty child and FSA arrays entirely, so both are
// optional here rather than defaulted: an absent "children" and an empty
// one mean the same thing to the tree, but only the server decides which
// it sends.
import { deleteJSON, type ApiResult } from './api'

export interface AdminFSA {
  href: string
  mRID: string
  description: string
  primacy: number
  programs?: string[]
  devices?: string[]
}

export interface AdminFSAList {
  fsas: AdminFSA[] | null
}

export interface TopologyFSA {
  id: string
  mRID: string
  description: string
  programs?: string[]
}

export interface TopologyNode {
  kind: string
  id: string
  label: string
  sfdi?: string
  lfdi?: string
  enabled?: boolean
  fsas?: TopologyFSA[]
  children?: TopologyNode[]
}

// nodeColor is the per-kind token for the four topology levels, used as an
// inline style value.
export function nodeColor(kind: string): string {
  switch (kind) {
    case 'SY':
      return 'var(--chart-1)'
    case 'FD':
      return 'var(--chart-2)'
    case 'SP':
      return 'var(--chart-3)'
    case 'DEV':
      return 'var(--text)'
    default:
      return 'var(--text)'
  }
}

// deviceIdFromHref takes the trailing path segment of an EndDevice href,
// which is the device id the FSA assignment routes take
// (POST /api/devices/{id}/fsa-assignment).
export function deviceIdFromHref(href: string): string {
  if (!href) return ''
  const i = href.lastIndexOf('/')
  return i < 0 ? href : href.substring(i + 1)
}

// unassignDeviceFsa takes a device off an FSA by the FSA's mRID, which is
// its identity on the wire. The server refuses with a 409 while the device
// still holds programs of its own under that FSA, naming them in the error.
export function unassignDeviceFsa(deviceId: string, fsaMRID: string): Promise<ApiResult<void>> {
  return deleteJSON(
    `/api/devices/${encodeURIComponent(deviceId)}/fsa-assignment?fsaHref=${encodeURIComponent(`/api/fsas/${fsaMRID}`)}`,
  )
}

// unassignFailure is the one wording of a refused unassign, shown wherever
// the call is made.
export function unassignFailure(res: { status: number; error: string }): string {
  return `Unassign failed (${res.status}): ${res.error}`
}
