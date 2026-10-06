// derstatus.ts turns the dashboard's per-DER reported status (decoded by the
// server's internal/derstatus) into the text, chips and hover the Devices tab
// and the Overview show. Everything here is what the device said about
// itself: "Connected" is the DER's own power connection to the Area EPS and
// never implies it is energized or exporting.

import type { DashboardDER, DashboardDevice, DerConnect } from './dashboard'

export interface Chip {
  label: string
  warn: boolean
}

// What one reported cell shows: chips (or plain text), the hover, freshness
// notes, and a read error. Both reported columns render this one shape.
export interface CellView {
  chips: Chip[]
  text: string
  title: string
  notes: string[]
  error: string
}

export const CONNECTED_TIP =
  "DER's own connection, as reported by the device. Does not mean energized or exporting."
export const STANDBY_TIP = 'Output may be energized while in service.'

export function hex(raw: number): string {
  return '0x' + raw.toString(16).toUpperCase().padStart(2, '0')
}

// anyConnected is the combined rule: a DER is connected if its primary
// field or the storage field beside it says so. Reading connect.connected
// alone would show a PV-plus-battery unit at night as Disconnected.
export function anyConnected(c: DerConnect | null): boolean {
  return c !== null && (c.connected || (c.also?.connected ?? false))
}

// connectChips: bit 0 always renders, since a clear bit 0 is a positive
// report of disconnection. The other set bits follow in bit order and
// reserved bits are shown, never dropped.
export function connectChips(c: DerConnect): Chip[] {
  const parts = c.also ? [c, c.also] : [c]
  const any = (pick: (p: DerConnect) => boolean) => parts.some(pick)
  const chips: Chip[] = [{ label: anyConnected(c) ? 'Connected' : 'Disconnected', warn: false }]
  if (any((p) => p.available)) chips.push({ label: 'Available', warn: false })
  if (any((p) => p.energized === true)) chips.push({ label: 'Energized', warn: false })
  if (any((p) => p.operating)) chips.push({ label: 'Operating', warn: false })
  if (any((p) => p.test)) chips.push({ label: 'Test', warn: true })
  if (any((p) => p.fault)) chips.push({ label: 'Fault', warn: true })
  const reserved = parts.reduce((bits, p) => bits | p.reservedBits, 0)
  if (reserved !== 0) chips.push({ label: `Reserved bits ${hex(reserved)}`, warn: true })
  return chips
}

function connectLine(c: DerConnect): string {
  return `${c.source} ${hex(c.raw)}${c.since > 0 ? ` (since ${new Date(c.since * 1000).toISOString()})` : ''}`
}

const INVERTER_LABELS: Record<number, string> = {
  0: 'N/A',
  1: 'Off',
  2: 'Sleeping / low output',
  3: 'Starting / on, no output',
  4: 'Running',
  5: 'Derated',
  6: 'Shutting down',
  7: 'Fault',
  8: 'Standby (service)',
  9: 'Test',
  10: 'Manufacturer-defined',
}

export function inverterLabel(code: number): string {
  return INVERTER_LABELS[code] ?? `Reserved (${code})`
}

// formatSeconds is a coarse duration for a reading's age.
export function formatSeconds(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

const chipText = (chips: Chip[]) => {
  const [first, ...rest] = chips
  return rest.length === 0 ? first.label : `${first.label} - ${rest.map((c) => c.label).join(', ')}`
}

interface One {
  chips: Chip[]
  text: string
  title: string
}

const NOT_REPORTED: One = { chips: [], text: 'Not reported', title: '' }

function connectOne(der: DashboardDER): One {
  if (!der.reported || der.connect === null) return NOT_REPORTED
  const chips = connectChips(der.connect)
  const lines = [CONNECTED_TIP, connectLine(der.connect)]
  if (der.connect.also) lines.push(connectLine(der.connect.also))
  return { chips, text: chipText(chips), title: lines.join('\n') }
}

function inverterOne(der: DashboardDER): One {
  if (!der.reported || der.inverter === null) return NOT_REPORTED
  const label = inverterLabel(der.inverter.code)
  const lines = [`inverterStatus ${der.inverter.code}`]
  if (der.inverter.code === 8) lines.push(STANDBY_TIP)
  const warn = der.inverter.code === 7 || der.inverter.code === 9
  return { chips: [{ label, warn }], text: label, title: lines.join('\n') }
}

// freshnessNotes covers the whole device. The age shown is the freshest
// reading's, so a device is not described as older than its newest report.
function freshnessNotes(device: DashboardDevice): string[] {
  const reported = device.ders.filter((d) => d.reported)
  const notes: string[] = []
  const aged = reported.filter((d) => !d.noReadingTime && !d.clockAhead)
  const age = aged.length > 0 ? Math.min(...aged.map((d) => d.ageSeconds)) : null
  if (reported.some((d) => d.noReadingTime)) notes.push('No reading time')
  if (reported.some((d) => d.clockAhead)) notes.push('Clock ahead')
  if (aged.some((d) => d.stale) && age !== null) notes.push(`Stale, ${formatSeconds(age)} old`)
  if (device.lastKnown) notes.push(age !== null ? `Last known, ${formatSeconds(age)} old` : 'Last known')
  return notes
}

function deviceView(device: DashboardDevice, one: (d: DashboardDER) => One, errorText: (e: string) => string): CellView {
  const error = device.derError ? errorText(device.derError) : ''
  const notes = freshnessNotes(device)
  if (device.ders.length === 0) {
    return { chips: [], text: error ? '' : 'No DERs', title: '', notes, error }
  }
  const views = device.ders.map(one)
  const signature = (v: One) => v.chips.map((c) => c.label).join('|') + '/' + v.text
  if (views.every((v) => signature(v) === signature(views[0]))) {
    const v = views[0]
    return { chips: v.chips, text: v.chips.length > 0 ? '' : v.text, title: v.title, notes, error }
  }
  const title = views.map((v, i) => `DER ${device.ders[i].id}: ${v.text}`).join('\n')
  return { chips: [{ label: 'mixed', warn: false }], text: '', title, notes, error }
}

export const deviceConnectionView = (device: DashboardDevice): CellView => deviceView(device, connectOne, (e) => `DER status unreadable: ${e}`)
export const deviceInverterView = (device: DashboardDevice): CellView => deviceView(device, inverterOne, () => 'Unreadable')

// derConnectedCounts is the Overview's DERs-reporting-connected stat. A DER
// counts as connected only on a fresh reading from a device that is talking:
// a stale or last-known "Connected" is not a live report. Stale DERs are
// counted separately so they are not silently absent.
export function derConnectedCounts(devices: DashboardDevice[]): { connected: number; stale: number; reported: number } {
  let connected = 0
  let stale = 0
  let reported = 0
  for (const device of devices) {
    for (const der of device.ders) {
      if (!der.reported || der.connect === null) continue
      reported++
      if (der.stale) stale++
      else if (!device.lastKnown && anyConnected(der.connect)) connected++
    }
  }
  return { connected, stale, reported }
}
