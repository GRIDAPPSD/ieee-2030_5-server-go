// Wire shapes for GET /api/derms/commitments?aggregatorLFDI=
// (internal/handler/admin_commitments.go). Every figure is shown as the
// route sent it: power and energy are magnitudes, direction names the flow,
// and a quantity the response does not carry is null, never zero.
import type { Interval } from './flowreservation'

export interface CommittedGrant {
  mRID: string
  edevId: string
  frqId: string
  window: Interval
  direction: string | null
  powerW: number | null
  energyWh: number | null
  energyRemainingWh: number | null
}

// targetW is opModTargetW as stored, discharge positive.
export interface CommittedControl {
  mRID: string
  edevId: string
  window: Interval
  targetW: number | null
}

export interface Commitments {
  aggregatorLFDI: string
  now: number
  grants: CommittedGrant[]
  plainControls: CommittedControl[]
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function isNumber(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

function isNullableNumber(v: unknown): boolean {
  return v === null || isNumber(v)
}

function isInterval(v: unknown): boolean {
  return isObject(v) && isNumber(v.start) && isNumber(v.duration)
}

function isGrant(v: unknown): boolean {
  return (
    isObject(v) &&
    typeof v.mRID === 'string' &&
    typeof v.edevId === 'string' &&
    isInterval(v.window) &&
    (v.direction === null || typeof v.direction === 'string') &&
    isNullableNumber(v.powerW) &&
    isNullableNumber(v.energyWh) &&
    isNullableNumber(v.energyRemainingWh)
  )
}

function isControl(v: unknown): boolean {
  return isObject(v) && typeof v.mRID === 'string' && typeof v.edevId === 'string' && isInterval(v.window) && isNullableNumber(v.targetW)
}

// A missing key is not a null: null means the route had no value, and an
// absent key means this is not the response the pane was written against.
export function isCommitments(v: unknown): v is Commitments {
  return (
    isObject(v) &&
    typeof v.aggregatorLFDI === 'string' &&
    isNumber(v.now) &&
    Array.isArray(v.grants) &&
    v.grants.every(isGrant) &&
    Array.isArray(v.plainControls) &&
    v.plainControls.every(isControl)
  )
}

// The route's own words for the direction; anything else, including null,
// is stated as not sent rather than guessed from the sign of the power.
export function grantDirectionWord(direction: string | null): string {
  if (direction === 'charge' || direction === 'discharge') return direction
  return 'direction not sent'
}
