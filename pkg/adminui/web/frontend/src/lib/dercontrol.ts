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
  programHref: string
  type: ControlType
  // Percent (0-100, maxLimW) or power factor (0.001-1.000, fixedPFInjectW).
  // Ignored for connect/disconnect.
  value: number | null
  excitation: boolean
  startNow: boolean
  // A datetime-local input value, used only when startNow is false.
  startAtLocal: string
  durationMinutes: number | null
  description: string
}

export type BuildResult =
  | { ok: true; body: ControlRequestBody; summary: string }
  | { ok: false; error: string }

function actionText(input: ControlFormInput): BuildResult | string {
  switch (input.type) {
    case 'connect':
      return 'Connect'
    case 'disconnect':
      return 'Disconnect'
    case 'maxLimW':
      if (input.value === null || input.value < 0 || input.value > 100) {
        return { ok: false, error: 'Limit must be 0 to 100 percent.' }
      }
      return `Limit to ${input.value.toFixed(2)}% of setMaxW`
    case 'fixedPFInjectW':
      if (input.value === null || input.value < 0.001 || input.value > 1) {
        return { ok: false, error: 'Power factor must be 0.001 to 1.000.' }
      }
      return `Set power factor to ${input.value.toFixed(3)} (${input.excitation ? 'under-excited' : 'over-excited'})`
  }
}

// buildControlRequest turns the form's raw input into the POST
// /api/der/controls body and a one-sentence confirmation summary, or an
// error for the form to show without posting anything. Range checks here
// mirror admin_dercontrol.go's buildCreateRequest/buildValue so a bad
// value is caught before Confirm, not after.
export function buildControlRequest(input: ControlFormInput): BuildResult {
  if (!input.programHref) return { ok: false, error: 'Pick a DER program first.' }
  if (input.durationMinutes === null || !Number.isFinite(input.durationMinutes) || input.durationMinutes <= 0) {
    return { ok: false, error: 'Duration must be a positive number of minutes.' }
  }

  const action = actionText(input)
  if (typeof action !== 'string') return action

  const body: ControlRequestBody = {
    derProgramHref: input.programHref,
    type: input.type,
    durationSeconds: Math.round(input.durationMinutes * 60),
  }

  if (input.description) {
    if (new TextEncoder().encode(input.description).length > MAX_DESCRIPTION_OCTETS) {
      return { ok: false, error: `Description must be at most ${MAX_DESCRIPTION_OCTETS} octets.` }
    }
    body.description = input.description
  }

  if (input.type === 'maxLimW' && input.value !== null) {
    body.maxLimW = Math.round(input.value * 100)
  } else if (input.type === 'fixedPFInjectW' && input.value !== null) {
    body.powerFactor = { displacement: Math.round(input.value * 1000), excitation: input.excitation }
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

  const summary = `${action}, starting ${when}, for ${input.durationMinutes} min.`
  return { ok: true, body, summary }
}

export function fmtTime(seconds: number): string {
  return new Date(seconds * 1000).toISOString()
}

// expectedDeviceReadTime is the latest time a device will have read the
// control, absent a push notification: creationTime plus the
// DERControlList pollRate.
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

// canCancel reports whether a control's derived status still allows
// cancelling it: an ended, cancelled or superseded control has nothing
// left to cancel.
export function canCancel(status: string): boolean {
  return status === 'scheduled' || status === 'active'
}
