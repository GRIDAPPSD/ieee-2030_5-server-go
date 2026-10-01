// Wire types for the Descriptor v2 payload that pkg/sep2admin/descriptor.go
// marshals. testdata/descriptor_v2.json pins the shape, and
// descriptorWire.test.ts checks these types against it at runtime.
//
// Section.kind and Cell.kind are plain strings, not closed unions: a
// server ahead of this build can send a kind this frontend does not
// know, and the renderer has to say so rather than the types hiding it.
// Every collection is [] on the wire, never null.

export interface DescriptorCell {
  kind: string
  text: string
  badge?: string
  datetime?: string
  href?: string
}

export type DescriptorRow = DescriptorCell[]

export interface DescriptorTableBody {
  columns: string[]
  rows: DescriptorRow[]
}

export interface DescriptorDefinitionEntry {
  key: string
  value: DescriptorCell
}

export interface DescriptorDefinitionGroup {
  heading: string
  entries: DescriptorDefinitionEntry[]
}

export interface DescriptorDefinitionListBody {
  groups: DescriptorDefinitionGroup[]
}

export interface DescriptorSection {
  kind: string
  heading: string
  prose: string[]
  empty: string
  body: unknown
}

export interface Descriptor {
  version: number
  sections: DescriptorSection[]
}

export interface PanelEntry {
  id: string
  label: string
}

// The closed badge set. A badge's class comes from this map and never
// from the wire text, so an unknown or hostile value cannot name a class.
const BADGE_CLASSES = {
  neutral: 'badge-neutral',
  info: 'badge-info',
  ok: 'badge-ok',
  warn: 'badge-warn',
  error: 'badge-error',
} as const

export function badgeClass(variant: string | undefined): string {
  if (variant !== undefined && Object.hasOwn(BADGE_CLASSES, variant)) {
    return BADGE_CLASSES[variant as keyof typeof BADGE_CLASSES]
  }
  return BADGE_CLASSES.neutral
}

// The rule pkg/sep2admin's checkHref enforces at encode, applied again as
// a second defence; href_cases.json holds the cases both sides run.
// Returns null for a refused href, and the caller shows the text unlinked.
export function safeHref(href: string | undefined): string | null {
  if (!href) return null
  for (let i = 0; i < href.length; i++) {
    const code = href.charCodeAt(i)
    if (code <= 0x20 || code === 0x7f || href[i] === '\\') return null
  }
  const lower = href.toLowerCase()
  if (lower.startsWith('http://') || lower.startsWith('https://')) {
    const rest = href.slice(href.indexOf('//') + 2)
    const authority = rest.slice(0, firstOf(rest, '/?#'))
    return authority === '' || authority.includes('@') ? null : href
  }
  if (href.startsWith('//')) return null
  return href.slice(0, firstOf(href, '/?#')).includes(':') ? null : href
}

function firstOf(s: string, chars: string): number {
  for (let i = 0; i < s.length; i++) if (chars.includes(s[i])) return i
  return s.length
}

// How often an open panel is re-read, and how long one read may take. The
// server bounds a View at 5s, so a read that outlasts 10s has lost its
// connection rather than its View.
export const PANEL_POLL_MS = 15000
export const PANEL_REQUEST_TIMEOUT_MS = 10000
