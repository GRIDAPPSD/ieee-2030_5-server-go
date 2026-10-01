// The write side of the flow reservation queue: POST .../answer, .../revise
// and .../cancel under /api/derms/flow-reservations/{edevId}/{frqId}. The
// page sends magnitudes and the operator's choice and nothing else. It never
// picks a sign, never decides whether a grant fits, and shows the server's
// refusal as the server gave it.
import { postJSON, type RequestOptions } from './api'
import { normalizeQueue, type FlowReservationEntry } from './flowreservation'

export type ActionKind = 'grant_as_asked' | 'grant_adjusted' | 'deny' | 'revise' | 'cancel'

// Every field is the raw text of an input. Empty means "not set": for a
// grant the server reads an absent interval, energy or power as "as asked".
export interface ActionForm {
  start: string
  duration: string
  energy: string
  power: string
  reason: string
  deny: boolean
}

export const REASON_MAX = 192

export interface ScaledBody {
  value: number
  multiplier: number
}

export interface WriteBody {
  decision?: 'grant' | 'deny'
  interval?: { start: number; duration: number }
  energy?: ScaledBody
  power?: ScaledBody
  reason?: string
}

export type BuiltAction = { ok: true; path: string; body: WriteBody } | { ok: false; error: string }

export function emptyForm(): ActionForm {
  return { start: '', duration: '', energy: '', power: '', reason: '', deny: false }
}

function writePath(entry: FlowReservationEntry, verb: 'answer' | 'revise' | 'cancel'): string {
  return (
    '/api/derms/flow-reservations/' +
    encodeURIComponent(entry.edevId) +
    '/' +
    encodeURIComponent(entry.frqId) +
    '/' +
    verb
  )
}

// parseStart reads a UTC instant written as 2026-10-01T12:00:00Z. Date.parse
// rolls 2026-02-31 over to March, so the instant must print back as typed.
function parseStart(text: string): number | null {
  const t = text.trim()
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?Z$/.test(t)) return null
  const ms = Date.parse(t)
  if (Number.isNaN(ms)) return null
  const iso = new Date(ms).toISOString()
  if (iso.slice(0, 16) !== t.slice(0, 16)) return null
  if (t.length === 20 && iso.slice(17, 19) !== t.slice(17, 19)) return null
  return Math.floor(ms / 1000)
}

// formatStart prints an epoch second as the form's start text, or '' when
// the server sent a value outside the range a Date can hold.
export function formatStart(epochSeconds: number): string {
  const when = new Date(epochSeconds * 1000)
  if (Number.isNaN(when.getTime())) return ''
  return when.toISOString().replace('.000Z', 'Z')
}

type Parsed<T> = { ok: true; value: T | undefined } | { ok: false; error: string }

function parseMagnitude(text: string, label: string): Parsed<ScaledBody> {
  const t = text.trim()
  if (t === '') return { ok: true, value: undefined }
  const n = Number(t)
  if (!/^\d+$/.test(t) || !Number.isSafeInteger(n)) {
    return { ok: false, error: label + ' must be a whole number of 0 or more, with no sign: the server applies the direction' }
  }
  return { ok: true, value: { value: n, multiplier: 0 } }
}

function parseInterval(form: ActionForm): Parsed<{ start: number; duration: number }> {
  const start = form.start.trim()
  const duration = form.duration.trim()
  if (start === '' && duration === '') return { ok: true, value: undefined }
  const s = parseStart(start)
  if (s === null) return { ok: false, error: 'start must be a UTC time such as 2026-10-01T12:00:00Z' }
  if (!/^\d+$/.test(duration) || Number(duration) < 1 || !Number.isSafeInteger(Number(duration))) {
    return { ok: false, error: 'duration must be a whole number of seconds, at least 1' }
  }
  return { ok: true, value: { start: s, duration: Number(duration) } }
}

// buildAction turns the operator's inputs into the request the server
// expects, or says what is wrong with them. It checks the shape of each
// input only; whether a value fits the request or the fleet is the
// server's decision and comes back as a refusal.
export function buildAction(kind: ActionKind, entry: FlowReservationEntry, form: ActionForm): BuiltAction {
  if (kind === 'grant_as_asked') return { ok: true, path: writePath(entry, 'answer'), body: { decision: 'grant' } }
  if (kind === 'deny') return { ok: true, path: writePath(entry, 'answer'), body: { decision: 'deny' } }

  const reason = form.reason.trim()
  if (Array.from(reason).length > REASON_MAX) {
    return { ok: false, error: 'reason is at most ' + REASON_MAX + ' characters' }
  }
  if (kind === 'cancel') {
    return { ok: true, path: writePath(entry, 'cancel'), body: reason === '' ? {} : { reason } }
  }

  const body: WriteBody = { decision: kind === 'revise' && form.deny ? 'deny' : 'grant' }
  if (body.decision === 'grant') {
    const interval = parseInterval(form)
    if (!interval.ok) return interval
    const energy = parseMagnitude(form.energy, 'energy')
    if (!energy.ok) return energy
    const power = parseMagnitude(form.power, 'power')
    if (!power.ok) return power
    if (interval.value !== undefined) body.interval = interval.value
    if (energy.value !== undefined) body.energy = energy.value
    if (power.value !== undefined) body.power = power.value
    if (kind === 'grant_adjusted' && Object.keys(body).length === 1) {
      return { ok: false, error: 'set an interval, energy or power, or use Grant as asked' }
    }
  }
  if (kind === 'revise' && reason !== '') body.reason = reason
  return { ok: true, path: writePath(entry, kind === 'revise' ? 'revise' : 'answer'), body }
}

// Plain words for each refusal code in the server's table. A code not
// listed here is shown with the server's own text.
const REFUSALS: Record<string, string> = {
  body_format: 'The request body was not valid.',
  unknown_field: 'The request body had a field the server does not know.',
  decision_invalid: 'The decision was not grant or deny.',
  value_negative: 'Energy and power are magnitudes and cannot be negative.',
  reason_too_long: 'The reason is too long.',
  reason_not_allowed: 'A reason is only accepted when revising or cancelling.',
  interval_outside_window: 'The interval is outside the window the aggregator asked for.',
  no_requested_window: 'The request named no window to grant inside.',
  no_requested_energy: 'The request named no energy, so energy cannot be set.',
  no_requested_power: 'The request named no power, so power cannot be set.',
  energy_exceeds_request: 'The energy is more than the aggregator asked for.',
  power_exceeds_request: 'The power is more than the aggregator asked for.',
  grant_zero_duration: 'A grant must last longer than zero seconds; deny the request instead.',
  request_not_found: 'The server has no such request.',
  already_answered: 'This request already has an answer.',
  request_cancelled: 'The aggregator cancelled this request.',
  not_answered: 'This request has no answer yet; deny it with an answer instead.',
  grant_not_live: 'This grant is not live: it was denied, cancelled or has ended.',
  fleet_window_committed: 'The new grant overlaps a grant or control the fleet already has.',
  execution_outside_interval: 'A control carrying out the grant would fall outside the new interval.',
  execution_exceeds_power: 'A control carrying out the grant is above the new power.',
  execution_exceeds_energy: 'The controls carrying out the grant use more than the new energy.',
  execution_reverses_grant: 'A control carrying out the grant would run in the opposite direction.',
}

interface RefusalBody {
  error?: unknown
  code?: unknown
  mRID?: unknown
  frqId?: unknown
}

function asRefusal(body: unknown): RefusalBody | null {
  return body !== null && typeof body === 'object' && !Array.isArray(body) ? (body as RefusalBody) : null
}

// describeRefusal shows a failed write with its code, and names the mRID
// and request id the body carries. A 404 or 405 without a refusal code is a
// route this server does not have, which is not the same as "not found".
// outcomeUnknown is a failure after which the write may still have been
// applied: no answer at all, or a server-side failure. Only a 503
// not_configured is a definite "nothing was done".
function outcomeUnknown(status: number, code: string | null): boolean {
  return status === 0 || (status >= 500 && !(status === 503 && code === 'not_configured'))
}

// Refusals that say the row on screen is out of date: the server's state has
// moved on, so the pane reloads rather than offer the same action again.
const STALE_CODES = new Set(['already_answered', 'grant_not_live', 'request_cancelled', 'not_answered', 'request_not_found'])

export function describeRefusal(status: number, error: string, body: unknown): string {
  const b = asRefusal(body)
  const code = b !== null && typeof b.code === 'string' && b.code !== '' ? b.code : null
  if (status === 503 && code === 'not_configured') return 'Answers are not enabled on this server (not_configured).'
  if ((status === 404 || status === 405) && code === null) return 'Answer API not available on this server yet.'
  if (outcomeUnknown(status, code)) {
    const said = status === 0 ? 'No answer from the server (' + error + ')' : 'The server answered ' + status + (code === null ? '' : ' (' + code + ')')
    return said + '. Outcome unknown: reloading the queue. Check the row before trying again.'
  }
  if (code === null) return 'The server refused the change (status ' + status + '): ' + error
  const words = REFUSALS[code] ?? error
  const names: string[] = []
  if (typeof b?.mRID === 'string' && b.mRID !== '') names.push('mRID ' + b.mRID)
  if (typeof b?.frqId === 'string' && b.frqId !== '') names.push('request ' + b.frqId)
  return words + ' (' + code + ')' + (names.length > 0 ? ' Names ' + names.join(', ') + '.' : '')
}

export type WriteResult =
  | { ok: true; entry: FlowReservationEntry }
  | { ok: false; message: string; kind: 'refused' | 'stale' | 'unknown' }

// viewFrom reads the request the server returns after a write: the whole
// FlowReservationView, as the body itself, for the request that was written.
function viewFrom(data: unknown, href: string): FlowReservationEntry | null {
  const parsed = normalizeQueue({ requests: [data] })
  if (!('queue' in parsed)) return null
  const entry = parsed.queue.requests[0]
  const ok = typeof entry.edevId === 'string' && typeof entry.frqId === 'string' && entry.requestHref === href
  return ok ? entry : null
}

export async function submitWrite(
  path: string,
  body: WriteBody,
  href: string,
  opts: RequestOptions,
): Promise<WriteResult> {
  const res = await postJSON<unknown>(path, body, opts)
  if (!res.ok) {
    const b = asRefusal(res.body)
    const code = b !== null && typeof b.code === 'string' ? b.code : null
    const message = describeRefusal(res.status, res.error, res.body)
    if (outcomeUnknown(res.status, code)) return { ok: false, message, kind: 'unknown' }
    return { ok: false, message, kind: code !== null && STALE_CODES.has(code) ? 'stale' : 'refused' }
  }
  const entry = viewFrom(res.data, href)
  if (entry === null) {
    return {
      ok: false,
      message: 'The server accepted the change but its reply could not be read. Reloading the queue; check the row.',
      kind: 'unknown',
    }
  }
  return { ok: true, entry }
}
