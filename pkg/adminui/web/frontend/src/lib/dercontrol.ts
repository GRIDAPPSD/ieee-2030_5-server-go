// Wire shapes and pure form/display logic for the admin DER control API
// (internal/handler/admin_dercontrol.go). Kept separate from the panel so
// the value encoding (percent -> hundredths, power factor -> thousandths)
// and the create-request validation are testable without mounting a
// component, the same split fleet.ts uses for FleetPane.

export type ControlType = 'connect' | 'disconnect' | 'maxLimW' | 'fixedPFInjectW'

export interface DERProgramView {
  href: string
  mRID: string
  description: string
  primacy: number
  derControlListHref: string | null
}

export interface DERProgramListResponse {
  device: string
  programs: DERProgramView[] | null
}

export interface FixedPowerFactorView {
  displacement: number
  excitation: boolean
  multiplier: number
}

export interface DERControlBaseView {
  opModConnect?: boolean
  opModEnergize?: boolean
  opModMaxLimW?: number
  opModFixedPFInjectW?: FixedPowerFactorView
}

export interface DERControlView {
  mRID: string
  href: string
  derProgramHref: string
  derControlListHref: string
  type: string
  description: string
  derControlBase: DERControlBaseView
  creationTime: number
  interval: { start: number; duration: number }
  eventStatus: { currentStatus: number; status: string; dateTime: number }
}

export interface DERControlCreated extends DERControlView {
  supersedes: string[]
  notificationAttempted: boolean
  persisted: boolean
}

export interface DERControlResponseCounts {
  total: number
  byStatus: Record<string, number>
}

export interface DERControlListItem extends DERControlView {
  responses: DERControlResponseCounts
}

export interface DERControlListResponse {
  device: string
  controls: DERControlListItem[] | null
}

// The DERControlList pollRate is a fixed 900 seconds
// (pkg/sep2srv/assembly/assembly.go registerDERRoutes), so a device that
// misses the push notification still polls the list within this many
// seconds of the control being stored.
export const POLL_RATE_SECONDS = 900

// Server-side bound on the description field (IEEE 2030.5-2018 Annex B.2
// String32), mirrored here so a too-long description fails before a
// round trip.
const MAX_DESCRIPTION_OCTETS = 32

export interface ControlRequestBody {
  derProgramHref: string
  type: ControlType
  durationSeconds: number
  description?: string
  maxLimW?: number
  powerFactor?: { displacement: number; excitation: boolean }
  startTime?: number
}

export interface ControlFormInput {
  // Shown in the confirmation sentence so the operator sees the target.
  deviceLabel: string
  programLabel: string
  programHref: string
  type: ControlType
  // Percent (0-100, at most 2 decimals, maxLimW) or power factor (0.001-1.000,
  // at most 3 decimals, fixedPFInjectW). Ignored for connect/disconnect.
  value: number | null
  excitation: boolean
  startNow: boolean
  // A datetime-local input value (no zone), used only when startNow is false.
  startAtLocal: string
  durationMinutes: number | null
  description: string
}

export type BuildResult =
  | { ok: true; body: ControlRequestBody; summary: string }
  | { ok: false; error: string }

// scaled returns value * factor as an integer, or null when value carries
// more decimals than the factor allows, so the confirmation and the posted
// body can never disagree by a silent rounding.
function scaled(value: number, factor: number): number | null {
  const n = value * factor
  const r = Math.round(n)
  return Math.abs(n - r) > 1e-7 ? null : r
}

function pct(hundredths: number): string {
  return (hundredths / 100).toFixed(2)
}

function pf(thousandths: number): string {
  return (thousandths / 1000).toFixed(3)
}

// buildControlRequest turns the form's raw input into the POST
// /api/der/controls body and a one-sentence confirmation summary, or an
// error for the form to show without posting anything. Range checks here
// mirror admin_dercontrol.go's buildCreateRequest/buildValue so a bad
// value is caught before Confirm, not after. The summary is written from
// the body's own numbers.
export function buildControlRequest(input: ControlFormInput): BuildResult {
  if (!input.programHref) return { ok: false, error: 'Pick a DER program first.' }
  if (input.durationMinutes === null || !Number.isFinite(input.durationMinutes) || input.durationMinutes <= 0) {
    return { ok: false, error: 'Duration must be a positive number of minutes.' }
  }
  const durationSeconds = Math.round(input.durationMinutes * 60)
  if (durationSeconds < 1) return { ok: false, error: 'Duration must be at least 1 second.' }

  const body: ControlRequestBody = {
    derProgramHref: input.programHref,
    type: input.type,
    durationSeconds,
  }
  let action: string
  switch (input.type) {
    case 'connect':
      action = 'Connect'
      break
    case 'disconnect':
      action = 'Disconnect'
      break
    case 'maxLimW': {
      if (input.value === null || input.value < 0 || input.value > 100) {
        return { ok: false, error: 'Limit must be 0 to 100 percent.' }
      }
      const h = scaled(input.value, 100)
      if (h === null) return { ok: false, error: 'Limit allows at most 2 decimals.' }
      body.maxLimW = h
      action = `Limit to ${pct(h)}% of setMaxW`
      break
    }
    case 'fixedPFInjectW': {
      if (input.value === null || input.value < 0.001 || input.value > 1) {
        return { ok: false, error: 'Power factor must be 0.001 to 1.000.' }
      }
      const t = scaled(input.value, 1000)
      if (t === null) return { ok: false, error: 'Power factor allows at most 3 decimals.' }
      body.powerFactor = { displacement: t, excitation: input.excitation }
      action = `Set power factor to ${pf(t)} (${input.excitation ? 'under-excited' : 'over-excited'})`
      break
    }
  }

  if (input.description) {
    if (new TextEncoder().encode(input.description).length > MAX_DESCRIPTION_OCTETS) {
      return { ok: false, error: `Description must be at most ${MAX_DESCRIPTION_OCTETS} octets.` }
    }
    body.description = input.description
  }

  let when = 'now'
  if (!input.startNow) {
    if (!input.startAtLocal) return { ok: false, error: 'Pick a start time, or choose Start now.' }
    const ms = Date.parse(input.startAtLocal)
    if (Number.isNaN(ms)) return { ok: false, error: 'Start time is not valid.' }
    const seconds = Math.floor(ms / 1000)
    body.startTime = seconds
    when = fmtTime(seconds)
  }

  const length = durationSeconds % 60 === 0 ? `${durationSeconds / 60} min` : `${durationSeconds} s`
  const summary = `${action} on device ${input.deviceLabel}, program ${input.programLabel}, starting ${when}, for ${length}.`
  return { ok: true, body, summary }
}

// keptControl reads the 500 body the create and cancel routes answer when a
// write could not be undone (DERControlIncomplete): the control it names may
// be live, so the operator needs its mRID and href.
export function keptControl(body: unknown): { mRID: string; href: string } | null {
  if (typeof body !== 'object' || body === null) return null
  const b = body as { controlKept?: unknown; mRID?: unknown; href?: unknown }
  if (b.controlKept !== true || typeof b.mRID !== 'string' || typeof b.href !== 'string') return null
  return { mRID: b.mRID, href: b.href }
}

export function fmtTime(seconds: number): string {
  return new Date(seconds * 1000).toISOString()
}

// expectedDeviceReadTime is a nominal estimate, never a delivery time: the
// stored time plus the DERControlList pollRate, for a device that polls on
// schedule and misses the push. An offline device never reads it.
export function expectedDeviceReadTime(creationTime: number): string {
  return fmtTime(creationTime + POLL_RATE_SECONDS)
}

// controlValueText renders a stored control's type-specific value for the
// controls table.
export function controlValueText(ctrl: DERControlView): string {
  const base = ctrl.derControlBase
  if (ctrl.type === 'maxLimW' && base.opModMaxLimW !== undefined) {
    return `${(base.opModMaxLimW / 100).toFixed(2)}%`
  }
  if (ctrl.type === 'fixedPFInjectW' && base.opModFixedPFInjectW) {
    const pf = base.opModFixedPFInjectW
    return `${(pf.displacement / 1000).toFixed(3)} (${pf.excitation ? 'under-excited' : 'over-excited'})`
  }
  return '-'
}

// canCancel reports whether a row still offers Cancel. The server derives
// no ended status (an ended control keeps reading active and cancelling it
// answers 409), so an active row qualifies while its interval has not ended
// on the SERVER's clock (serverNowSeconds, from the response's Date header).
// With no server time the row keeps Cancel and the server decides: hiding
// Cancel on a live curtailment because of a skewed browser clock is the
// unsafe direction, while a refused cancel is loud.
export function canCancel(status: string, start: number, duration: number, serverNowSeconds: number | null): boolean {
  if (status === 'scheduled') return true
  if (status !== 'active') return false
  return serverNowSeconds === null || serverNowSeconds < start + duration
}
