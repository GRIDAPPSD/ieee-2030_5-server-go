// The Overview events timeline's wire shape (GET /api/derms/control-events,
// internal/handler admin_control_events.go), its narrowing, and the pure
// layout the timeline draws: lanes in the power chart's order, bar spans on
// the shared time range, labels in words.
import { DIRECT_KEY, aggregatorKeyLabels, formatClock, formatKw, isStrings, type PowerAnswer } from './power'

// 'unknown' is a status the server sends when it cannot derive one.
export type ControlEventStatus = 'scheduled' | 'active' | 'completed' | 'cancelled' | 'superseded' | 'unknown'
export type ControlEventDirection = 'discharge' | 'charge'

export interface ControlEvent {
  group: string
  // 'unassigned' is a control whose devices were all unassigned; its group is
  // UNASSIGNED_KEY and its deviceIDs are empty.
  groupKind: 'aggregator' | 'direct' | 'unassigned'
  scope: 'device' | 'fsa'
  fsaID?: string
  deviceIDs: string[]
  mrid: string
  origin: 'admin' | 'derms'
  direction: ControlEventDirection | null
  // Signed watts, discharge positive; null when the control has no target.
  targetW: number | null
  modes: string[]
  status: ControlEventStatus
  // Epoch seconds.
  start: number
  end: number
}

export interface ControlEventsAnswer {
  windowStart: number
  windowEnd: number
  // How far past windowEnd the events reach, in seconds; null on a server that
  // predates the look-ahead, which then ends the chart at now.
  aheadSeconds: number | null
  events: ControlEvent[]
}

// Whether the server has answered with a look-ahead, kept here so the card
// reopens on the range it left. An answer states the exact look-ahead; until
// one does, a server known to send one is taken to look ahead one window.
let aheadSupported = false

export const lookAheadFor = (windowSeconds: number): number => (aheadSupported ? windowSeconds : 0)

export function rememberLookAhead(supported: boolean): void {
  aheadSupported = supported
}

// forgetLookAhead resets the memory; the tests call it.
export const forgetLookAhead = (): void => rememberLookAhead(false)

export const UNASSIGNED_KEY = 'unassigned'

export const controlEventsPath = (windowSeconds: number): string =>
  `/api/derms/control-events?window=${windowSeconds}&scope=all`

const STATUSES: readonly string[] = ['scheduled', 'active', 'completed', 'cancelled', 'superseded', 'unknown']

function isEvent(v: unknown): v is ControlEvent {
  if (typeof v !== 'object' || v === null) return false
  const e = v as Record<string, unknown>
  return (
    typeof e.group === 'string' &&
    (e.groupKind === 'aggregator' || e.groupKind === 'direct' || e.groupKind === 'unassigned') &&
    (e.scope === 'device' || e.scope === 'fsa') &&
    (e.fsaID === undefined || typeof e.fsaID === 'string') &&
    isStrings(e.deviceIDs) &&
    typeof e.mrid === 'string' &&
    (e.origin === 'admin' || e.origin === 'derms') &&
    (e.direction === null || e.direction === 'discharge' || e.direction === 'charge') &&
    (e.targetW === null || (typeof e.targetW === 'number' && Number.isFinite(e.targetW))) &&
    isStrings(e.modes) &&
    typeof e.status === 'string' &&
    STATUSES.includes(e.status) &&
    typeof e.start === 'number' &&
    typeof e.end === 'number'
  )
}

// parseControlEvents refuses a reply whole when any field the timeline reads
// has the wrong type, so a malformed reply never draws as a partial one.
export function parseControlEvents(value: unknown): ControlEventsAnswer | null {
  if (typeof value !== 'object' || value === null) return null
  const v = value as Record<string, unknown>
  if (typeof v.windowStart !== 'number' || typeof v.windowEnd !== 'number' || !Array.isArray(v.events)) return null
  // The server sends null for a control with no modes; it reads as none.
  const events = v.events.map((e) => (typeof e === 'object' && e !== null && (e as { modes?: unknown }).modes === null ? { ...e, modes: [] } : e))
  if (!events.every(isEvent)) return null
  if (v.aheadEnd !== undefined && (typeof v.aheadEnd !== 'number' || v.aheadEnd < v.windowEnd)) return null
  const aheadSeconds = typeof v.aheadEnd === 'number' ? v.aheadEnd - v.windowEnd : null
  return { windowStart: v.windowStart, windowEnd: v.windowEnd, aheadSeconds, events }
}

// The bar text in words, split so a narrow bar can drop the detail and keep
// the lead: the direction, then the target ("discharge", " 5.0 kW"), or the
// mode names when the control has no target. word is true when the lead is
// the direction word, which a bar never hides.
export function eventLabelParts(e: ControlEvent): { lead: string; detail: string; word: boolean } {
  if (e.targetW !== null) {
    const kw = formatKw(Math.abs(e.targetW))
    return e.direction === null ? { lead: kw, detail: '', word: false } : { lead: e.direction, detail: ` ${kw}`, word: true }
  }
  if (e.modes.length > 0) return { lead: e.modes.join(', '), detail: '', word: false }
  return { lead: e.direction ?? 'control', detail: '', word: e.direction !== null }
}

export const eventLabel = (e: ControlEvent): string => {
  const { lead, detail } = eventLabelParts(e)
  return lead + detail
}

const time = (seconds: number) => formatClock(seconds * 1000)

// The hover and assistive text: the label, who sent it, its status and span.
export function eventTitle(e: ControlEvent): string {
  const origin = e.origin === 'derms' ? 'DERMS' : 'admin'
  return `${eventLabel(e)}, ${origin}, ${e.status}, ${time(e.start)} to ${time(e.end)}`
}

export interface Lane {
  key: string
  label: string
}

// One lane per aggregator in the order the power chart draws them, then
// Direct, labelled as the legend labels them. A group the power answer does
// not list still gets a lane after those, so no event goes undrawn, and
// "Unassigned" comes last, only when an event sits in it.
export function lanesFor(answer: PowerAnswer, events: ControlEvent[], labelOf: (lfdi: string) => string): Lane[] {
  const per = answer.aggregatorEnergy
  const keys = per !== null && per.length > 0 ? per.map((e) => e.key) : [...answer.aggregators, DIRECT_KEY]
  const known = new Set(keys)
  for (const e of events) {
    if (e.group !== UNASSIGNED_KEY && !known.has(e.group)) {
      known.add(e.group)
      keys.push(e.group)
    }
  }
  if (events.some((e) => e.group === UNASSIGNED_KEY)) keys.push(UNASSIGNED_KEY)
  const labels = aggregatorKeyLabels(keys, (key) => (key === UNASSIGNED_KEY ? 'Unassigned' : labelOf(key)))
  return keys.map((key, i) => ({ key, label: labels[i] }))
}

export interface Bar {
  event: ControlEvent
  // Percent of the shared range, clipped to it.
  left: number
  width: number
  // The stacking row inside the lane, from 0.
  row: number
}

// barsFor places a lane's events on the range [rangeStartMs, rangeEndMs] and
// stacks overlapping ones into rows. An event wholly outside the range is
// dropped.
export function barsFor(events: ControlEvent[], rangeStartMs: number, rangeEndMs: number): Bar[] {
  const span = rangeEndMs - rangeStartMs
  if (span <= 0) return []
  const sorted = events
    .filter((e) => e.end * 1000 > rangeStartMs && e.start * 1000 < rangeEndMs)
    .sort((a, b) => a.start - b.start || a.end - b.end)
  const rowEnds: number[] = []
  return sorted.map((event) => {
    const from = Math.max(event.start * 1000, rangeStartMs)
    const to = Math.min(event.end * 1000, rangeEndMs)
    let row = rowEnds.findIndex((end) => end <= event.start)
    if (row < 0) row = rowEnds.length
    rowEnds[row] = event.end
    return { event, left: ((from - rangeStartMs) / span) * 100, width: ((to - from) / span) * 100, row }
  })
}

// Where "now" falls on the range, in percent, or null before it starts. A
// "now" past the end sits at the end.
export function nowPercent(nowMs: number, rangeStartMs: number, rangeEndMs: number): number | null {
  const span = rangeEndMs - rangeStartMs
  if (span <= 0 || nowMs < rangeStartMs) return null
  return (Math.min(nowMs, rangeEndMs) - rangeStartMs) / span * 100
}
