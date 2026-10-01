// Wire shapes and pure display helpers for GET /api/derms/flow-reservations
// (#764). The page shows what the server sends: it never grants, flips a
// sign, or decides which response is the tip.

export interface ScaledValue {
  value: number
  multiplier: number
}

export interface Interval {
  start: number
  duration: number
}

export interface EventStatus {
  currentStatus: number
  status: string
  dateTime: number
}

export interface Actor {
  kind: string
  admission: string | null
  principal: string | null
  at: number
}

export interface ExecutionView {
  mRID: string
  href: string
  derControlListHref: string
  interval: Interval
  targetW: ScaledValue | null
  eventStatus: EventStatus | null
}

export type Direction = 'charge' | 'discharge'

export interface ResponseView {
  id: string
  href: string
  mRID: string
  subject: string
  creationTime: number
  interval: Interval
  energyAvailable: ScaledValue | null
  powerAvailable: ScaledValue | null
  direction: string | null
  eventStatus: EventStatus | null
  cancelReason: string | null
  answeredBy: Actor | null
  cancelledBy: Actor | null
  executions: ExecutionView[]
  energyCommittedWh: number | null
  energyRemainingWh: number | null
}

export interface FlowReservationRequest {
  mRID: string
  creationTime: number
  requestStatus: string
  intervalRequested: Interval
  energyRequested: ScaledValue | null
  powerRequested: ScaledValue | null
  direction: string | null
}

export interface FlowReservationEntry {
  edevId: string
  frqId: string
  requestHref: string
  aggregatorLFDI: string
  state: string
  deadlineAt: number | null
  request: FlowReservationRequest
  responses: ResponseView[]
  tip: ResponseView | null
}

export interface FlowReservationQueue {
  aggregatorLFDI: string
  now?: number
  deadlineSeconds?: number
  persisted?: boolean
  requests: FlowReservationEntry[]
}

// scaledNumber applies the 2030.5 multiplier (a power of ten). null stays
// null: an absent quantity is missing, not zero.
export function scaledNumber(q: ScaledValue | null | undefined): number | null {
  if (q === null || q === undefined) return null
  return q.value * Math.pow(10, q.multiplier)
}

// formatQuantity prints the magnitude with its unit. Direction is a separate
// word taken from the server's own direction field, so no sign is rewritten
// here: the minus on a discharge energy is replaced by the word beside it,
// never by a flipped number.
export function formatQuantity(n: number | null, unit: string): string {
  if (n === null) return 'missing'
  return `${Math.round(Math.abs(n)).toLocaleString('en-US')} ${unit}`
}

export function directionLabel(direction: string | null | undefined): string {
  if (direction === 'charge' || direction === 'discharge') return direction
  return 'direction missing'
}

// remainingSeconds is the countdown to a deadline measured on the server's
// clock: serverNow is the payload's `now` and elapsedSeconds is how long
// this page has held that payload, so browser clock skew does not move it.
// null means the server sent no deadline.
export function remainingSeconds(
  deadlineAt: number | null,
  serverNow: number,
  elapsedSeconds: number,
): number | null {
  if (deadlineAt === null) return null
  return deadlineAt - (serverNow + elapsedSeconds)
}

export function formatCountdown(remaining: number | null): string {
  if (remaining === null) return 'deadline missing'
  if (remaining <= 0) return 'deadline passed'
  return `${remaining}s to deadline`
}

export function formatInterval(i: Interval): string {
  const start = new Date(i.start * 1000).toISOString().replace('T', ' ').replace('.000Z', ' UTC')
  const mins = i.duration / 60
  const dur = Number.isInteger(mins) ? `${mins} min` : `${i.duration} s`
  return `${start} for ${dur}`
}

// formatActor names who acted. A null actor is shown as unknown rather
// than omitted, since a missing answerer is itself information.
export function formatActor(a: Actor | null): string {
  if (a === null) return 'unknown'
  const kind = a.kind.replace(/_/g, ' ')
  const principal = a.principal === null ? '' : ` ${a.principal.length > 20 ? a.principal.slice(0, 20) + '...' : a.principal}`
  const admission = a.admission === null ? '' : ` via ${a.admission}`
  return `${kind}${principal}${admission}`
}

// historyResponses is every response except the one the server named as
// the tip, in the order the server sent them.
export function historyResponses(entry: FlowReservationEntry): ResponseView[] {
  const tipId = entry.tip?.id
  return entry.responses.filter((r) => r.id !== tipId)
}

// normalizeQueues accepts one aggregator's object or a list of them: the
// fixture shows one object, and #764 has not fixed the multi-aggregator shape.
export function normalizeQueues(data: unknown): FlowReservationQueue[] | null {
  const list = Array.isArray(data) ? data : [data]
  for (const q of list) {
    if (q === null || typeof q !== 'object' || !Array.isArray((q as FlowReservationQueue).requests)) {
      return null
    }
  }
  return list as FlowReservationQueue[]
}
