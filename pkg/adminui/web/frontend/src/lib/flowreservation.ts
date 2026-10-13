// Wire shapes and pure display helpers for GET
// /api/derms/flow-reservations?aggregatorLFDI=... (#764). The page shows
// what the server sends: it never grants, flips a sign, or decides which
// response is the tip.

// multiplier may be absent on a malformed body; scaledNumber then reports
// the quantity as missing instead of computing NaN.
export interface ScaledValue {
  value: number
  multiplier?: number
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
  interval: Interval | null
  targetW: ScaledValue | null
  eventStatus: EventStatus | null
}

export interface ResponseView {
  id: string
  href: string
  mRID: string
  subject: string
  creationTime: number
  interval: Interval | null
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
  intervalRequested: Interval | null
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
  requestCancelled?: boolean
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

function isFiniteNumber(n: unknown): n is number {
  return typeof n === 'number' && Number.isFinite(n)
}

// scaledNumber applies the 2030.5 multiplier (a power of ten). null stays
// null: an absent quantity, or one with a missing value or multiplier, is
// missing, not zero and not NaN.
export function scaledNumber(q: ScaledValue | null | undefined): number | null {
  if (q === null || q === undefined || typeof q !== 'object') return null
  if (!isFiniteNumber(q.value) || !isFiniteNumber(q.multiplier)) return null
  return q.value * Math.pow(10, q.multiplier)
}

// formatQuantity prints the value as the server sent it, sign included.
// The sign is the only direction there is when the direction field is
// null, so it is never dropped and never flipped; the direction word beside
// it comes from the server's own field.
export function formatQuantity(n: number | null | undefined, unit: string): string {
  if (!isFiniteNumber(n)) return 'missing'
  const rounded = Math.round(n)
  const text = Math.abs(rounded).toLocaleString('en-US')
  return (rounded < 0 ? '-' : '') + text + ' ' + unit
}

// formatMeasured is formatQuantity for a metered figure: a non-zero value
// below 10 units keeps one decimal, and one too small for that reads "<0.1",
// so it never shows as the "0" of a measured zero. A true 0 still reads 0.
export function formatMeasured(n: number | null | undefined, unit: string): string {
  if (!isFiniteNumber(n)) return 'missing'
  const abs = Math.abs(n)
  if (abs === 0 || abs >= 9.95) return formatQuantity(n, unit)
  const sign = n < 0 ? '-' : ''
  if (abs < 0.05) return sign + '<0.1 ' + unit
  return sign + abs.toFixed(1) + ' ' + unit
}

export function directionLabel(direction: string | null | undefined): string {
  if (direction === 'charge' || direction === 'discharge') return direction
  return 'direction missing'
}

// remainingSeconds is the countdown to a deadline measured on the server's
// clock: serverNow is the payload's `now` (or the response Date header when
// the payload has none) and elapsedSeconds is how long this page has held
// that payload, so browser clock skew does not move it. null means there is
// nothing to count down from.
export function remainingSeconds(
  deadlineAt: number | null | undefined,
  serverNow: number | null,
  elapsedSeconds: number,
): number | null {
  if (!isFiniteNumber(deadlineAt) || serverNow === null) return null
  return deadlineAt - (serverNow + elapsedSeconds)
}

export function formatCountdown(
  deadlineAt: number | null | undefined,
  serverNow: number | null,
  elapsedSeconds: number,
): string {
  if (!isFiniteNumber(deadlineAt)) return 'deadline missing'
  const remaining = remainingSeconds(deadlineAt, serverNow, elapsedSeconds)
  if (remaining === null) return 'server time unavailable'
  if (remaining <= 0) return 'deadline passed'
  return remaining + 's to deadline'
}

export function formatInterval(i: Interval | null | undefined): string {
  if (i === null || i === undefined || !isFiniteNumber(i.start) || !isFiniteNumber(i.duration)) {
    return 'interval missing'
  }
  // toISOString throws RangeError outside the Date range (about 8.64e15 ms).
  const when = new Date(i.start * 1000)
  if (Number.isNaN(when.getTime())) return 'interval time invalid'
  const start = when.toISOString().replace('T', ' ').replace('.000Z', ' UTC')
  const mins = i.duration / 60
  const dur = Number.isInteger(mins) ? mins + ' min' : i.duration + ' s'
  return start + ' for ' + dur
}

// formatActor names who acted. A null actor is shown as unknown rather
// than omitted, since a missing answerer is itself information.
export function formatActor(a: Actor | null | undefined): string {
  if (a === null || a === undefined || typeof a.kind !== 'string') return 'unknown'
  const kind = a.kind.replace(/_/g, ' ')
  const principal =
    typeof a.principal !== 'string' ? '' : ' ' + (a.principal.length > 20 ? a.principal.slice(0, 20) + '...' : a.principal)
  const admission = typeof a.admission !== 'string' ? '' : ' via ' + a.admission
  return kind + principal + admission
}

// historyResponses is every response except the one the server named as
// the tip, in the order the server sent them.
export function historyResponses(entry: FlowReservationEntry): ResponseView[] {
  const tipId = entry.tip?.id
  return entry.responses.filter((r) => r.id !== tipId)
}

function isObject(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object' && !Array.isArray(v)
}

function objectOrNull(v: unknown): boolean {
  return v === null || v === undefined || isObject(v)
}

function dupKey(keys: string[]): string | null {
  const seen = new Set<string>()
  for (const k of keys) {
    if (seen.has(k)) return k
    seen.add(k)
  }
  return null
}

function checkResponse(r: unknown, where: string): string | null {
  if (!isObject(r)) return where + ' is not an object'
  if (typeof r.id !== 'string') return where + ' has no id'
  if (!objectOrNull(r.interval)) return where + ' interval is not an object'
  if (!objectOrNull(r.eventStatus)) return where + ' eventStatus is not an object'
  if (!objectOrNull(r.answeredBy) || !objectOrNull(r.cancelledBy)) return where + ' actor is not an object'
  if (!Array.isArray(r.executions)) return where + ' has no executions list'
  const mRIDs: string[] = []
  for (const ex of r.executions) {
    if (!isObject(ex) || typeof ex.mRID !== 'string') return where + ' has a malformed execution'
    if (!objectOrNull(ex.interval) || !objectOrNull(ex.eventStatus)) return where + ' has a malformed execution'
    mRIDs.push(ex.mRID)
  }
  const dup = dupKey(mRIDs)
  if (dup !== null) return where + ' repeats execution ' + dup
  return null
}

// nullAbsent reads every nullable ResponseView field that a server may
// omit instead of sending null as null, so a template that tests for null
// cannot mistake an absent key for a present one.
const NULLABLE_RESPONSE_FIELDS = [
  'interval',
  'energyAvailable',
  'powerAvailable',
  'direction',
  'eventStatus',
  'cancelReason',
  'answeredBy',
  'cancelledBy',
  'energyCommittedWh',
  'energyRemainingWh',
]

function nullAbsent(r: Record<string, unknown>): Record<string, unknown> {
  const out = { ...r }
  for (const k of NULLABLE_RESPONSE_FIELDS) out[k] = r[k] ?? null
  return out
}

// normalizeResponse is normalizeQueue's per-response check for a reader that
// holds a response outside a queue: the response with absent nullable keys
// read as null, or the first problem found.
export function normalizeResponse(r: unknown, where: string): { response: ResponseView } | { error: string } {
  const bad = checkResponse(r, where)
  if (bad !== null) return { error: bad }
  return { response: nullAbsent(r as Record<string, unknown>) as unknown as ResponseView }
}

// normalizeQueue checks, before anything renders, every field the pane
// reads, because a throw during render leaves the pane stuck on its loading
// state with nothing shown. It returns a copy with an undefined tip read as
// null, or the first problem found.
export function normalizeQueue(data: unknown): { queue: FlowReservationQueue } | { error: string } {
  if (!isObject(data)) return { error: 'queue is not an object' }
  if (!Array.isArray(data.requests)) return { error: 'queue has no requests list' }
  const keys: string[] = []
  const requests: FlowReservationEntry[] = []
  for (const [n, e] of data.requests.entries()) {
    const where = 'request ' + n
    if (!isObject(e)) return { error: where + ' is not an object' }
    if (typeof e.requestHref !== 'string') return { error: where + ' has no requestHref' }
    if (typeof e.aggregatorLFDI !== 'string') return { error: where + ' has no aggregatorLFDI' }
    if (typeof e.state !== 'string') return { error: where + ' has no state' }
    if (!isObject(e.request)) return { error: where + ' has no request' }
    if (!objectOrNull(e.request.intervalRequested)) return { error: where + ' interval is not an object' }
    if (!Array.isArray(e.responses)) return { error: where + ' has no responses list' }
    const ids: string[] = []
    for (const [m, r] of e.responses.entries()) {
      const bad = checkResponse(r, where + ' response ' + m)
      if (bad !== null) return { error: bad }
      ids.push((r as { id: string }).id)
    }
    const dupResp = dupKey(ids)
    if (dupResp !== null) return { error: where + ' repeats response ' + dupResp }
    const tip = e.tip === undefined ? null : e.tip
    if (tip !== null) {
      const bad = checkResponse(tip, where + ' tip')
      if (bad !== null) return { error: bad }
    }
    keys.push(e.requestHref)
    requests.push({
      ...(e as unknown as FlowReservationEntry),
      deadlineAt: (e.deadlineAt ?? null) as number | null,
      responses: (e.responses as Record<string, unknown>[]).map(nullAbsent) as unknown as ResponseView[],
      tip: tip === null ? null : (nullAbsent(tip as Record<string, unknown>) as unknown as ResponseView),
    })
  }
  const dup = dupKey(keys)
  if (dup !== null) return { error: 'queue repeats request ' + dup }
  return { queue: { ...(data as unknown as FlowReservationQueue), requests } }
}

// parseFleetLFDIs reads the aggregator list from GET /api/derms/fleets.
// Null when the body is not a list of fleets or repeats an aggregator,
// since each LFDI keys a block of the pane.
export function parseFleetLFDIs(data: unknown): string[] | null {
  if (!Array.isArray(data)) return null
  const out: string[] = []
  for (const f of data) {
    if (!isObject(f) || typeof f.aggregatorLFDI !== 'string') return null
    out.push(f.aggregatorLFDI)
  }
  return dupKey(out) === null ? out : null
}
