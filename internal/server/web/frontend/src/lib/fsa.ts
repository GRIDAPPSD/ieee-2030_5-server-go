// Wire shapes for the admin FSA and topology endpoints
// (internal/handler/admin_fsa.go, internal/handler/admin_topology.go).
// The list endpoint wraps its array in an object, and the topology
// endpoint omits empty child and FSA arrays entirely, so both are
// optional here rather than defaulted: an absent "children" and an empty
// one mean the same thing to the tree, but only the server decides which
// it sends.

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

// nodeColor is the per-kind colour the tree has always used for the four
// topology levels.
export function nodeColor(kind: string): string {
  switch (kind) {
    case 'SY':
      return '#93c5fd'
    case 'FD':
      return '#a78bfa'
    case 'SP':
      return '#f472b6'
    case 'DEV':
      return '#e2e8f0'
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
