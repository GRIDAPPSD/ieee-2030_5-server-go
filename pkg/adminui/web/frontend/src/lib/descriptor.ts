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

export interface PanelPickerInfo {
  max: number
}

export interface PanelStreamInfo {
  maxLen: number
  charset: string
}

export interface PanelEntry {
  id: string
  label: string
  picker?: PanelPickerInfo
  stream?: PanelStreamInfo
  // Present when the plane serves panel actions; the fields come from
  // GET /api/ui/panels/{id}/actions.
  actions?: { id: string; label: string }[]
  // Present for a page panel: the same-origin path (/apps/{id}/) the
  // shell frames in place of a descriptor.
  page?: string
}

// A page is framed only when it is a path that parses to this origin.
// The URL parser strips tab, LF and CR anywhere, so "/<TAB>/host" would
// pass a prefix check yet resolve off-origin; control characters and
// whitespace are refused first, then the parsed origin is compared.
export function isSameOriginPath(page: string): boolean {
  if (!page.startsWith('/') || /[\x00-\x20\x7f]/.test(page)) return false
  try {
    return new URL(page, window.location.origin).origin === window.location.origin
  } catch {
    return false
  }
}

export interface PickerChoice {
  id: string
  label: string
}

export interface PickerChoices {
  max: number
  choices: PickerChoice[]
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

// What a panel, an action or a card says when the admin session is missing.
export const SIGN_IN_AGAIN = 'Admin session required. Sign in again.'

// How often an open panel is re-read, and how long one read may take. The
// server bounds a View at 5s, so a read that outlasts 10s has lost its
// connection rather than its View.
export const PANEL_POLL_MS = 15000

export const PANEL_REQUEST_TIMEOUT_MS = 10000

export interface DescriptorChartSeries {
  name: string
  points: [number, number][]
}

// How a series is drawn, which the wire does not carry: a series with no type
// is a line; direction picks the discharge or charge palette for a bar.
export type ChartSeriesType = 'line' | 'bar'
export type ChartDirection = 'discharge' | 'charge'

export interface DrawnChartSeries extends DescriptorChartSeries {
  type?: ChartSeriesType
  direction?: ChartDirection
  // A bar series drawn in this slot of the chart palette instead of the
  // direction colors; the direction then shows by side of zero and hatch.
  colorIndex?: number
  // A stable key for the series, so two series with one display name stay two.
  id?: string
  // Extra legend hover text, such as the LFDI behind a device name.
  hint?: string
}

export interface DescriptorChartBody {
  unit: string
  series: DescriptorChartSeries[]
}

export interface DrawnChartBody {
  unit: string
  series: DrawnChartSeries[]
}

// Narrows a /choices reply. A reply that does not match is refused whole,
// so a malformed entry never reaches the control.
export function parseChoices(value: unknown): PickerChoices | null {
  if (typeof value !== 'object' || value === null) return null
  const { max, choices } = value as Partial<PickerChoices>
  if (typeof max !== 'number' || !Array.isArray(choices)) return null
  for (const c of choices) {
    if (typeof c !== 'object' || c === null || typeof c.id !== 'string' || typeof c.label !== 'string') return null
  }
  return { max, choices }
}

const SELECTION_KEY_PREFIX = 'adminui.picker.'

// The stored selection is per viewer and per panel, never in the URL. Both
// directions swallow storage failures (private mode, a blocked store, a
// full quota) so the panel works without storage. Corrupt or foreign
// content reads as no selection.
export function readSelection(panelId: string, max: number): string[] {
  try {
    const raw = globalThis.localStorage.getItem(SELECTION_KEY_PREFIX + panelId)
    if (raw === null) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    const ids: string[] = []
    for (const v of parsed) {
      if (typeof v !== 'string') return []
      if (!ids.includes(v)) ids.push(v)
    }
    return ids.slice(0, max)
  } catch {
    return []
  }
}

export function writeSelection(panelId: string, ids: string[]): void {
  try {
    globalThis.localStorage.setItem(SELECTION_KEY_PREFIX + panelId, JSON.stringify(ids))
  } catch {
    // Storage unavailable: the selection still applies for this session.
  }
}
