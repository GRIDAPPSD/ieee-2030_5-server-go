// Wire shapes and pure logic for the DERMS dispatch pane: GET
// /api/derms/grants?aggregatorLFDI=...&live=true (#764) and POST
// /api/der/controls (#566). The page sends the operator's target as typed
// and the grant it executes, and shows the server's refusal as given. It
// never flips a sign and never decides whether a control fits a grant.
import { keptControl, type DERControlCreated } from './dercontrol'
import { postJSON, type RequestOptions } from './api'
import type { FleetDevice } from './fleet'
import { normalizeResponse, scaledNumber, type ResponseView, type ScaledValue } from './flowreservation'

export interface GrantView {
  edevId: string
  frqId: string
  response: ResponseView
  suggestedTargetW: ScaledValue | null
}

export interface GrantList {
  aggregatorLFDI: string
  now: number | null
  grants: GrantView[]
}

function isObject(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object' && !Array.isArray(v)
}

// parseGrants checks every field the pane reads before anything renders,
// and reports the first problem rather than rendering a partial list.
export function parseGrants(data: unknown): { list: GrantList } | { error: string } {
  if (!isObject(data)) return { error: 'grants reply is not an object' }
  if (!Array.isArray(data.grants)) return { error: 'grants reply has no grants list' }
  const grants: GrantView[] = []
  const seen = new Set<string>()
  for (const [n, g] of data.grants.entries()) {
    const where = 'grant ' + n
    if (!isObject(g)) return { error: where + ' is not an object' }
    if (typeof g.edevId !== 'string' || typeof g.frqId !== 'string') return { error: where + ' has no edevId or frqId' }
    const parsed = normalizeResponse(g.response, where + ' response')
    if ('error' in parsed) return parsed
    if (typeof parsed.response.mRID !== 'string' || parsed.response.mRID === '') return { error: where + ' has no mRID' }
    if (seen.has(parsed.response.id)) return { error: 'grants repeat response ' + parsed.response.id }
    seen.add(parsed.response.id)
    const target = g.suggestedTargetW
    grants.push({
      edevId: g.edevId,
      frqId: g.frqId,
      response: parsed.response,
      suggestedTargetW: isObject(target) ? (target as unknown as ScaledValue) : null,
    })
  }
  return {
    list: {
      aggregatorLFDI: typeof data.aggregatorLFDI === 'string' ? data.aggregatorLFDI : '',
      now: typeof data.now === 'number' && Number.isFinite(data.now) ? data.now : null,
      grants,
    },
  }
}

export interface DeviceChoice {
  id: string
  label: string
  // Empty when the device is known only by the id a grant carries.
  lfdi: string
}

// fleetDeviceChoices lists the devices of a fleet the page can address: the
// fleet read names devices by LFDI, and only the dashboard list maps an LFDI
// to the id the control and program routes take. A device the dashboard does
// not know is left out, not guessed at.
export function fleetDeviceChoices(
  fleetDevices: FleetDevice[],
  dashboard: { sfdi: string; lfdi: string; href: string }[],
  idOf: (href: string) => string,
): DeviceChoice[] {
  const out: DeviceChoice[] = []
  for (const fd of fleetDevices) {
    const known = dashboard.find((d) => d.lfdi.toUpperCase() === fd.lfdi.toUpperCase())
    if (known !== undefined) out.push({ id: idOf(known.href), label: known.sfdi, lfdi: known.lfdi })
  }
  return out
}

export interface TargetBody {
  value: number
  multiplier: number
}

// encodeTarget turns the typed watts into opModTargetW's value and
// multiplier: the smallest multiplier from 0 to 9 at which the value is a
// whole int16. The sign is the operator's, passed through unchanged.
export function encodeTarget(text: string): { ok: true; target: TargetBody } | { ok: false; error: string } {
  const t = text.trim()
  if (!/^-?\d+$/.test(t)) return { ok: false, error: 'Target power must be a whole number of watts, with its sign.' }
  const n = Number(t)
  if (!Number.isSafeInteger(n)) return { ok: false, error: 'Target power is out of range.' }
  for (let m = 0; m <= 9; m++) {
    const v = n / Math.pow(10, m)
    if (Number.isInteger(v) && v >= -32768 && v <= 32767) return { ok: true, target: { value: v, multiplier: m } }
  }
  return { ok: false, error: 'Target power must fit -32768 to 32767 at a power-of-ten multiplier of 0 to 9.' }
}

function parseStartUTC(text: string): number | null {
  const t = text.trim()
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?Z$/.test(t)) return null
  const ms = Date.parse(t)
  if (Number.isNaN(ms)) return null
  const iso = new Date(ms).toISOString()
  if (iso.slice(0, 16) !== t.slice(0, 16)) return null
  if (t.length === 20 && iso.slice(17, 19) !== t.slice(17, 19)) return null
  return Math.floor(ms / 1000)
}

export interface DispatchForm {
  programHref: string
  // Raw input text; empty start means "start now" and the server decides.
  start: string
  duration: string
  power: string
  // The mRID of the grant being executed, or '' for a plain dispatch.
  grantMRID: string
}

export interface DispatchBody {
  derProgramHref: string
  type: 'targetW'
  targetW: TargetBody
  durationSeconds: number
  startTime?: number
  executesGrant?: string
}

export function buildDispatch(form: DispatchForm): { ok: true; body: DispatchBody } | { ok: false; error: string } {
  if (form.programHref === '') return { ok: false, error: 'Pick a DER program first.' }
  const target = encodeTarget(form.power)
  if (!target.ok) return target
  const duration = form.duration.trim()
  if (!/^\d+$/.test(duration) || Number(duration) < 1 || !Number.isSafeInteger(Number(duration))) {
    return { ok: false, error: 'Duration must be a whole number of seconds, at least 1.' }
  }
  const body: DispatchBody = {
    derProgramHref: form.programHref,
    type: 'targetW',
    targetW: target.target,
    durationSeconds: Number(duration),
  }
  if (form.start.trim() !== '') {
    const start = parseStartUTC(form.start)
    if (start === null) return { ok: false, error: 'Start must be a UTC time such as 2026-10-01T12:00:00Z, or empty to start now.' }
    body.startTime = start
  }
  if (form.grantMRID !== '') body.executesGrant = form.grantMRID
  return { ok: true, body }
}

// START_MARGIN_SECONDS is how far ahead of the server's clock a running
// grant's prefilled start is put, so the server does not refuse it as past.
export const START_MARGIN_SECONDS = 30

function utc(seconds: number): string {
  const when = new Date(seconds * 1000)
  return Number.isNaN(when.getTime()) ? '' : when.toISOString().replace('.000Z', 'Z')
}

// grantPrefill is what picking a grant puts in the form: its interval and the
// server's suggested target, as text. A value the server did not send stays
// empty rather than becoming a default. A grant that has started is prefilled
// from now plus a margin to the grant's own end, never from a past start, and
// note says so. nowSeconds is the server's clock.
export function grantPrefill(
  g: GrantView,
  nowSeconds: number,
): { start: string; duration: string; power: string; note: string } {
  const i = g.response.interval
  const watts = scaledNumber(g.suggestedTargetW)
  const power = watts !== null && Number.isInteger(watts) ? String(watts) : ''
  if (i === null || !Number.isFinite(i.start) || !Number.isFinite(i.duration)) {
    return { start: '', duration: '', power, note: '' }
  }
  const earliest = Math.floor(nowSeconds) + START_MARGIN_SECONDS
  if (i.start >= earliest) return { start: utc(i.start), duration: String(i.duration), power, note: '' }
  const left = i.start + i.duration - earliest
  if (left < 1) {
    return {
      start: '',
      duration: '',
      power,
      note: 'This grant has ended or is about to, so no interval is prefilled.',
    }
  }
  return {
    start: utc(earliest),
    duration: String(left),
    power,
    note: `This grant has already started: the start is now plus ${START_MARGIN_SECONDS} s and the duration runs to the grant's end.`,
  }
}

export type CreateResult =
  | { ok: true; control: DERControlCreated }
  | { ok: false; kind: 'refused' | 'unknown'; message: string }

function refusalFields(body: unknown): { code: string | null; mRID: string | null } {
  if (!isObject(body)) return { code: null, mRID: null }
  return {
    code: typeof body.code === 'string' && body.code !== '' ? body.code : null,
    mRID: typeof body.mRID === 'string' && body.mRID !== '' ? body.mRID : null,
  }
}

// describeCreateFailure words a failed create. A 409 is shown with the
// server's own text (which names the event or bound), its code, and the mRID
// it names. A status 0 or 5xx is an unknown outcome: the control may exist.
export function describeCreateFailure(status: number, error: string, body: unknown): CreateResult & { ok: false } {
  const kept = keptControl(body)
  if (kept !== null) {
    return {
      ok: false,
      kind: 'unknown',
      message: `The control was kept and may be live: ${kept.mRID} (${kept.href}). Check the controls list and cancel it if it is not wanted.`,
    }
  }
  if (status === 0 || status >= 500) {
    const said = status === 0 ? 'No answer from the server (' + error + ')' : 'The server answered ' + status + ': ' + error
    return { ok: false, kind: 'unknown', message: said + '. Outcome unknown: reloading. Check the controls list before sending again.' }
  }
  const f = refusalFields(body)
  const code = f.code === null ? '' : ' (' + f.code + ')'
  const names = f.mRID === null ? '' : ' Names ' + f.mRID + '.'
  const prefix = status === 409 ? 'The server refused this control: ' : 'The server refused the request: '
  return { ok: false, kind: 'refused', message: prefix + error + code + '.' + names }
}

export async function submitDispatch(body: DispatchBody, opts: RequestOptions): Promise<CreateResult> {
  const res = await postJSON<unknown>('/api/der/controls', body, opts)
  if (!res.ok) return describeCreateFailure(res.status, res.error, res.body)
  const c = res.data
  const readable =
    isObject(c) &&
    typeof c.mRID === 'string' &&
    isObject(c.interval) &&
    isObject(c.eventStatus) &&
    isObject(c.derControlBase) &&
    typeof c.derProgramHref === 'string' &&
    Array.isArray(c.supersedes)
  if (!readable) {
    return {
      ok: false,
      kind: 'unknown',
      message: 'The server accepted the control but its reply could not be read. Reloading; check the controls list before sending again.',
    }
  }
  return { ok: true, control: c as unknown as DERControlCreated }
}
