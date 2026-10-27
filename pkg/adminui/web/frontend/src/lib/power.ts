// The Overview power card's wire shape (GET /api/derms/power, internal/handler
// admin_power.go), its narrowing, and the last answer kept per scope and
// window. The cache lives in this module so it outlives the card: the shell
// unmounts the card when the viewer leaves the Overview and the answer must
// still be here when they come back.
import { isChartBody } from './chart'
import type { DescriptorChartBody, DrawnChartBody } from './descriptor'

export interface PowerWindow {
  seconds: number
  label: string
  // The bucket length the server picks for this window; the card polls once
  // per bucket until an answer states its own.
  bucketSeconds: number
}

export const POWER_WINDOWS: PowerWindow[] = [
  { seconds: 900, label: '15 min', bucketSeconds: 5 },
  { seconds: 3600, label: '1 h', bucketSeconds: 5 },
  { seconds: 21600, label: '6 h', bucketSeconds: 30 },
  { seconds: 86400, label: '24 h', bucketSeconds: 120 },
]

export const ALL_SCOPE = 'all'

export interface PowerDeviceFlags {
  lfdi: string
  directionUnknown: boolean
  concurrentMirrors: boolean
  nonDERWatts: boolean
}

export interface PowerAnswer {
  aggregators: string[]
  bucketSeconds: number
  energyChart: DescriptorChartBody
  devices: PowerDeviceFlags[]
  notReporting: string[]
  // Watts, export-positive; null while latestMissing names a device, never a
  // partial sum.
  latestW: number | null
  latestAt: number | null
  latestMissing: string[]
}

export function powerPath(scope: string, windowSeconds: number): string {
  const q = new URLSearchParams({ window: String(windowSeconds) })
  if (scope === ALL_SCOPE) q.set('scope', ALL_SCOPE)
  else q.set('aggregatorLFDI', scope)
  return `/api/derms/power?${q.toString()}`
}

const isStrings = (v: unknown): v is string[] => Array.isArray(v) && v.every((s) => typeof s === 'string')

function isFlags(v: unknown): v is PowerDeviceFlags {
  if (typeof v !== 'object' || v === null) return false
  const d = v as Record<string, unknown>
  return (
    typeof d.lfdi === 'string' &&
    typeof d.directionUnknown === 'boolean' &&
    typeof d.concurrentMirrors === 'boolean' &&
    typeof d.nonDERWatts === 'boolean'
  )
}

// parsePower refuses a reply whole when any field the card reads has the
// wrong type, so a malformed reply never reaches the chart as a partial one.
export function parsePower(value: unknown): PowerAnswer | null {
  if (typeof value !== 'object' || value === null) return null
  const v = value as Record<string, unknown>
  const latestW = v.latestW ?? null
  const latestAt = v.latestAt ?? null
  if (
    !isStrings(v.aggregators) ||
    typeof v.bucketSeconds !== 'number' ||
    !isChartBody(v.energyChart) ||
    !Array.isArray(v.devices) ||
    !v.devices.every(isFlags) ||
    !isStrings(v.notReporting) ||
    !isStrings(v.latestMissing) ||
    (latestW !== null && typeof latestW !== 'number') ||
    (latestAt !== null && typeof latestAt !== 'number')
  ) {
    return null
  }
  return {
    aggregators: v.aggregators,
    bucketSeconds: v.bucketSeconds,
    energyChart: v.energyChart,
    devices: v.devices,
    notReporting: v.notReporting,
    latestW,
    latestAt,
    latestMissing: v.latestMissing,
  }
}

export interface PowerEntry {
  answer: PowerAnswer
  // Browser clock, epoch milliseconds, when the answer arrived.
  at: number
}

// Four windows by every aggregator and "all" would otherwise grow with the
// fleet; the oldest-kept answer goes first once this many are held.
export const MAX_KEPT_ANSWERS = 32

const answers = new Map<string, PowerEntry>()
let choice = { scope: ALL_SCOPE, window: POWER_WINDOWS[0].seconds }

const keyOf = (scope: string, windowSeconds: number) => `${scope}|${windowSeconds}`

export const cachedPower = (scope: string, windowSeconds: number): PowerEntry | undefined =>
  answers.get(keyOf(scope, windowSeconds))

export function keepPower(scope: string, windowSeconds: number, entry: PowerEntry): void {
  const key = keyOf(scope, windowSeconds)
  // Re-inserting moves the key to the newest position in Map order.
  answers.delete(key)
  answers.set(key, entry)
  while (answers.size > MAX_KEPT_ANSWERS) answers.delete(answers.keys().next().value as string)
}

// The scope and window last chosen, so a return to the tab opens on the same
// chart it left.
export const lastPowerChoice = () => choice

export function rememberPowerChoice(scope: string, windowSeconds: number): void {
  choice = { scope, window: windowSeconds }
}

// forgetPower resets the kept answers and the choice; the tests call it.
export function forgetPower(): void {
  answers.clear()
  choice = { scope: ALL_SCOPE, window: POWER_WINDOWS[0].seconds }
}

export const powerBucketMs = (windowSeconds: number): number =>
  (POWER_WINDOWS.find((w) => w.seconds === windowSeconds)?.bucketSeconds ?? 120) * 1000

// kW to one decimal; the server reports watts.
export const formatKw = (watts: number): string => `${(watts / 1000).toFixed(1)} kW`

// The names of the energy chart's two series, as the server sends them.
export const DISCHARGED_SERIES = 'Discharged'
export const CHARGED_SERIES = 'Charged'

export interface EnergyTotals {
  discharged: number
  charged: number
}

// The window's energy in kWh, summed over the Discharged and Charged series
// of the energy chart (discharge at or above zero, charge at or below).
export function energyTotals(chart: DescriptorChartBody): EnergyTotals {
  const sum = (name: string) => chart.series.find((s) => s.name === name)?.points.reduce((acc, p) => acc + p[1], 0) ?? 0
  return { discharged: sum(DISCHARGED_SERIES), charged: sum(CHARGED_SERIES) }
}

// The energy chart drawn as bars: Discharged above zero, Charged below, each
// with its direction palette.
export function energyBars(chart: DescriptorChartBody): DrawnChartBody {
  return {
    ...chart,
    series: chart.series.map((s) => ({ ...s, type: 'bar', direction: s.name === CHARGED_SERIES ? 'charge' : 'discharge' })),
  }
}

export const formatKwh = (kwh: number): string => `${kwh.toFixed(4)} kWh`
