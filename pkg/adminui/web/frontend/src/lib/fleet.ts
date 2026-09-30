// Wire shapes and pure display helpers for GET /api/derms/fleets
// (internal/handler/admin_fleet.go). One entry per aggregator; each field a
// pointer on the Go side is optional here for the same reason, an omitted
// JSON key rather than a synthesized value.

export interface FleetMeasurement {
  value: number
  readingTime: number
  qualityFlags?: number
}

export interface FleetDeviceMeasurements {
  p?: FleetMeasurement
  q?: FleetMeasurement
  v?: FleetMeasurement
  f?: FleetMeasurement
}

export interface FleetDeviceStatus {
  connected?: boolean
  operationalMode?: number
  alarmStatus?: number
  stateOfCharge?: number
  readingTime?: number
}

export interface FleetDeviceAvailability {
  statWAvail?: number
  statVarAvail?: number
  readingTime?: number
}

export interface FleetDevice {
  lfdi: string
  status?: FleetDeviceStatus
  availability?: FleetDeviceAvailability
  measurements: FleetDeviceMeasurements
}

// FleetSum mirrors admin_fleet.go's FleetSum: a fleet-wide total over
// devices that reported the value, with the counts that did not (never
// reported at all) or went stale. Sum is 0 whenever nothing contributed to
// it, which is why sumFigure below never renders sum.sum on its own.
export interface FleetSum {
  sum: number
  unreported: number
  stale: number
}

export interface FleetRollup {
  deviceCount: number
  connected: number
  alarmed: number
  stale: number
  p: FleetSum
  q: FleetSum
  statWAvail: FleetSum
  statVarAvail: FleetSum
}

export interface Fleet {
  aggregatorLFDI: string
  devices: FleetDevice[]
  rollup: FleetRollup
}

// unreportedStatusCount counts devices that never sent a DERStatus at all
// (status omitted). Distinct from rollup.stale, which counts a DERStatus
// that IS present but has gone stale: a device with no status is neither
// connected, alarmed, nor stale, so the rollup does not count it in any of
// those three, and this fills the fourth status count the DERMS tab needs
// (#671 acceptance criteria) straight from the device list's own presence
// or absence of the field, with no staleness threshold re-derived here.
export function unreportedStatusCount(fleet: Fleet): number {
  return fleet.devices.filter((d) => d.status === undefined).length
}

// SumFigure is what a fleet-wide FleetSum renders as, once its trap is
// handled: contributing is deviceCount minus unreported minus stale, the
// same partition addToSum (admin_fleet.go) produces, so contributing === 0
// exactly when sum.sum is a zero nothing added to, never a measured zero.
export interface SumFigure {
  kind: 'reporting' | 'none'
  value: number
  unreported: number
  stale: number
  // ageSeconds is the age of the newest reading among devices that
  // reported this quantity at all, which can include a reading the rollup
  // excluded from sum.sum for being stale: the per-device response carries
  // no per-quantity stale flag of its own, only the rollup's counts do, so
  // this is the newest KNOWN reading behind the figure, not a promise that
  // it is the newest CONTRIBUTING one.
  ageSeconds: number | null
}

export function sumFigure(
  fleet: Fleet,
  sum: FleetSum,
  newestReadingTime: number | null,
  nowSeconds: number,
): SumFigure {
  const contributing = fleet.rollup.deviceCount - sum.unreported - sum.stale
  return {
    kind: contributing > 0 ? 'reporting' : 'none',
    value: sum.sum,
    unreported: sum.unreported,
    stale: sum.stale,
    ageSeconds: newestReadingTime === null ? null : Math.max(0, nowSeconds - newestReadingTime),
  }
}

// directionWord names the export-positive sign convention the server
// already applied (admin_fleet.go's considerMeasurement): positive is
// exporting, negative is importing, zero carries no direction word. The
// word is decided on the ROUNDED magnitude, the same rounding formatValue
// applies to the number beside it: an unrounded 0.4 W read as positive
// would print "0 W exporting", a word the displayed "0 W" does not back.
export function directionWord(watts: number): string {
  if (Math.round(Math.abs(watts)) === 0) return ''
  return watts > 0 ? 'exporting' : 'importing'
}

export function newestPReadingTime(fleet: Fleet): number | null {
  const times = fleet.devices
    .map((d) => d.measurements.p?.readingTime)
    .filter((t): t is number => t !== undefined)
  return times.length ? Math.max(...times) : null
}

// newestAvailReadingTime covers both statWAvail and statVarAvail: they
// share one DERAvailability.readingTime per device (admin_fleet.go's
// FleetDeviceAvailability), so there is one age for the pair, not two.
export function newestAvailReadingTime(fleet: Fleet): number | null {
  const times = fleet.devices
    .map((d) => d.availability?.readingTime)
    .filter((t): t is number => t !== undefined)
  return times.length ? Math.max(...times) : null
}

export function formatValue(value: number): string {
  return Math.round(Math.abs(value)).toLocaleString('en-US')
}

export function formatContributionNote(fig: SumFigure): string {
  const parts: string[] = []
  if (fig.unreported > 0) parts.push(`${fig.unreported} unreported`)
  if (fig.stale > 0) parts.push(`${fig.stale} stale`)
  return parts.length ? ` (${parts.join(', ')})` : ''
}

export function formatAge(seconds: number | null): string {
  if (seconds === null) return 'unknown'
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}
