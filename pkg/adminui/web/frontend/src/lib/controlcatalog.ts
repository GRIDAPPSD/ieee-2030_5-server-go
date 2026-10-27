// The control function catalog as the form consumes it
// (GET /api/der/control-functions) and the pure logic that turns a filled
// form into the `functions` member of a control request.
//
// A row is read by its kind alone. The catalog also serves a prose `value`
// text; it is deliberately absent from CatalogRow so nothing here can read it,
// and no function name is spelled in this file, so a new catalog row needs no
// frontend change.

import { fetchJSON } from './api'
import { localDateTimeSeconds } from './dercontrol'

export interface CatalogField {
  name: string
  kind: 'boolean' | 'integer'
  required?: boolean
  min?: number
  max?: number
}

export interface CatalogRow {
  name: string
  description: string
  // The kinds the form has a component for are boolean, integer, object and
  // curve. Any other string is an unsupported kind: shown, never sent.
  kind: string
  min?: number
  max?: number
  fields?: CatalogField[]
  curveType?: number
}

export interface CatalogResponse {
  functions: CatalogRow[]
}

export interface CurveInfo {
  id: string
  curveType: number
  description: string
}

export interface CurveListResponse {
  curves: CurveInfo[]
}

// What each kind's field component edits: a checkbox, the text of a number
// input, a map of those for an object, and a curve id.
export type FieldValue = boolean | string | Record<string, boolean | string>

export function isSupportedKind(kind: string): boolean {
  return kind === 'boolean' || kind === 'integer' || kind === 'object' || kind === 'curve'
}

// initialValue is what a field component starts with: booleans false,
// everything else empty. A required boolean therefore always has a value.
export function initialValue(row: CatalogRow): FieldValue {
  switch (row.kind) {
    case 'boolean':
      return false
    case 'object': {
      const out: Record<string, boolean | string> = {}
      // An optional boolean starts unset (''), so it is sent only once the
      // operator has touched it; a required one starts false.
      for (const f of row.fields ?? []) out[f.name] = f.kind === 'boolean' && f.required ? false : ''
      return out
    }
    default:
      return ''
  }
}

const WHOLE_NUMBER = /^-?[0-9]+$/

function rangeText(min: number | undefined, max: number | undefined): string {
  if (min !== undefined && max !== undefined) return ` from ${min} to ${max}`
  if (min !== undefined) return ` of at least ${min}`
  if (max !== undefined) return ` of at most ${max}`
  return ''
}

type Parsed<T> = { ok: true; value: T } | { ok: false; error: string }

function parseInteger(label: string, text: string, min?: number, max?: number): Parsed<number> {
  const t = text.trim()
  const n = Number(t)
  if (!WHOLE_NUMBER.test(t) || !Number.isSafeInteger(n) || (min !== undefined && n < min) || (max !== undefined && n > max)) {
    return { ok: false, error: `${label} must be a whole number${rangeText(min, max)}.` }
  }
  return { ok: true, value: n }
}

// buildValue turns one ticked row's field value into the request value for
// its kind, or says why it cannot.
export function buildValue(row: CatalogRow, value: FieldValue | undefined): Parsed<unknown> {
  switch (row.kind) {
    case 'boolean':
      return { ok: true, value: value === true }
    case 'integer':
      return parseInteger(row.name, typeof value === 'string' ? value : '', row.min, row.max)
    case 'curve': {
      const id = typeof value === 'string' ? value.trim() : ''
      if (id === '') return { ok: false, error: `${row.name}: pick a curve.` }
      return { ok: true, value: id }
    }
    case 'object': {
      const given = typeof value === 'object' ? value : {}
      const out: Record<string, boolean | number> = {}
      for (const f of row.fields ?? []) {
        const v = given[f.name]
        if (f.kind === 'boolean') {
          if (typeof v === 'boolean') out[f.name] = v
          else if (f.required) out[f.name] = false
          continue
        }
        const text = typeof v === 'string' ? v.trim() : ''
        if (text === '') {
          if (f.required) return { ok: false, error: `${row.name}: ${f.name} is required.` }
          continue
        }
        const p = parseInteger(`${row.name}: ${f.name}`, text, f.min, f.max)
        if (!p.ok) return p
        out[f.name] = p.value
      }
      return { ok: true, value: out }
    }
    default:
      return { ok: false, error: `${row.name}: this kind of value is not supported by this page.` }
  }
}

export type FunctionsResult =
  | { ok: true; functions: Record<string, unknown>; names: string[] }
  | { ok: false; error: string }

// buildFunctions builds the request's `functions` member from the ticked
// rows, in catalog order. At least one row must be ticked.
export function buildFunctions(rows: CatalogRow[], ticked: Record<string, boolean>, values: Record<string, FieldValue>): FunctionsResult {
  const functions: Record<string, unknown> = {}
  const names: string[] = []
  for (const row of rows) {
    if (!ticked[row.name]) continue
    const v = buildValue(row, values[row.name])
    if (!v.ok) return v
    functions[row.name] = v.value
    names.push(row.name)
  }
  if (names.length === 0) return { ok: false, error: 'Tick at least one function.' }
  return { ok: true, functions, names }
}

// functionsText is a control's functions as one line of name=value pairs,
// for the confirmation sentence and the table.
export function functionsText(functions: Record<string, unknown> | null | undefined): string {
  const entries = Object.entries(functions ?? {})
  if (entries.length === 0) return '-'
  return entries.map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(', ')
}

// curvesOfType keeps the stored curves a curve field may pick.
export function curvesOfType(curves: CurveInfo[], curveType: number | undefined): CurveInfo[] {
  return curves.filter((c) => c.curveType === curveType)
}

export interface DeviceControlBody {
  kind: 'scheduled' | 'default'
  functions: Record<string, unknown>
  startTime?: number
  durationSeconds?: number
  description?: string
}

export interface ControlFormInput {
  mode: 'scheduled' | 'default'
  // Names what the control is for in the summary, such as "device 123" or "FSA a, program b".
  target: string
  functions: Record<string, unknown>
  startNow: boolean
  // A datetime-local input value (no zone), used only when startNow is false.
  startAtLocal: string
  durationMinutes: number | null
  description: string
}

export type BuildResult = { ok: true; body: DeviceControlBody; summary: string } | { ok: false; error: string }

// Server-side bound on the description (IEEE 2030.5-2018 Annex B.2 String32),
// mirrored so a too-long description fails before a round trip.
const MAX_DESCRIPTION_OCTETS = 32

function fmtIso(seconds: number): string {
  return new Date(seconds * 1000).toISOString()
}

// buildDeviceControl turns the form into the POST /api/devices/{id}/controls
// body and a one-sentence confirmation summary written from the body's own
// values, or an error for the form to show without posting anything.
export function buildDeviceControl(input: ControlFormInput): BuildResult {
  const what = functionsText(input.functions)
  if (input.mode === 'default') {
    return {
      ok: true,
      body: { kind: 'default', functions: input.functions },
      summary: `Set the default control ${what} on ${input.target}. It is in force now, has no end, and devices send no responses.`,
    }
  }

  if (input.durationMinutes === null || !Number.isFinite(input.durationMinutes) || input.durationMinutes <= 0) {
    return { ok: false, error: 'Duration must be a positive number of minutes.' }
  }
  const durationSeconds = Math.round(input.durationMinutes * 60)
  if (durationSeconds < 1) return { ok: false, error: 'Duration must be at least 1 second.' }

  const body: DeviceControlBody = { kind: 'scheduled', functions: input.functions, durationSeconds }
  if (input.description) {
    if (new TextEncoder().encode(input.description).length > MAX_DESCRIPTION_OCTETS) {
      return { ok: false, error: `Description must be at most ${MAX_DESCRIPTION_OCTETS} octets.` }
    }
    body.description = input.description
  }

  let when = 'now'
  if (!input.startNow) {
    if (!input.startAtLocal) return { ok: false, error: 'Pick a start time, or choose Start now.' }
    const seconds = localDateTimeSeconds(input.startAtLocal)
    if (seconds === null) return { ok: false, error: 'Start time is not valid.' }
    body.startTime = seconds
    when = fmtIso(seconds)
  }
  const length = durationSeconds % 60 === 0 ? `${durationSeconds / 60} min` : `${durationSeconds} s`
  return {
    ok: true,
    body,
    summary: `Schedule ${what} on ${input.target}, starting ${when}, for ${length}.`,
  }
}

// primacyText words the primacy the server chose for a device-only program,
// from the reply's own two fields.
export function primacyText(primacy: number, reason: string): string {
  return `Primacy ${primacy}: ${reason}`
}

export type Loaded<T> = { ok: true; data: T } | { ok: false; error: string }

// loadCatalog reads the control function catalog, for every form that sets
// control values: the control window and the program form.
export async function loadCatalog(): Promise<Loaded<CatalogRow[]>> {
  const res = await fetchJSON<CatalogResponse>('/api/der/control-functions')
  if (!res.ok) return { ok: false, error: res.error }
  return { ok: true, data: res.data.functions ?? [] }
}

// loadCurves reads the stored curves a curve field may pick.
export async function loadCurves(): Promise<Loaded<CurveInfo[]>> {
  const res = await fetchJSON<CurveListResponse>('/api/der/curves')
  if (!res.ok) return { ok: false, error: `Stored curves could not be read: ${res.error}` }
  return { ok: true, data: res.data.curves ?? [] }
}

export interface FieldState {
  ticked: Record<string, boolean>
  values: Record<string, FieldValue>
}

// emptyFieldState is every row unticked with its initial value.
export function emptyFieldState(rows: CatalogRow[]): FieldState {
  const state: FieldState = { ticked: {}, values: {} }
  for (const row of rows) {
    state.ticked[row.name] = false
    state.values[row.name] = initialValue(row)
  }
  return state
}

function textOf(v: unknown): string {
  return typeof v === 'number' ? String(v) : ''
}

// fieldStateFromFunctions is the inverse of buildFunctions: it ticks each
// supported row the stored functions name and fills its field from the
// stored value, so a saved default reads back into the form it was made
// with. A stored name the catalog lacks, or a row of an unsupported kind, is
// left unticked.
export function fieldStateFromFunctions(rows: CatalogRow[], functions: Record<string, unknown> | undefined): FieldState {
  const state = emptyFieldState(rows)
  for (const row of rows) {
    if (!functions || !(row.name in functions) || !isSupportedKind(row.kind)) continue
    const v = functions[row.name]
    state.ticked[row.name] = true
    switch (row.kind) {
      case 'boolean':
        state.values[row.name] = v === true
        break
      case 'integer':
        state.values[row.name] = textOf(v)
        break
      case 'curve':
        state.values[row.name] = typeof v === 'string' ? v : ''
        break
      case 'object': {
        const given = v && typeof v === 'object' ? (v as Record<string, unknown>) : {}
        const out: Record<string, boolean | string> = {}
        for (const f of row.fields ?? []) {
          const x = given[f.name]
          if (f.kind === 'boolean') out[f.name] = typeof x === 'boolean' ? x : f.required ? false : ''
          else out[f.name] = textOf(x)
        }
        state.values[row.name] = out
        break
      }
    }
  }
  return state
}

// uneditableFunctions is the part of a stored default the form has no field
// for: a name the catalog lacks, or a row of an unsupported kind. A form that
// saves a default sends these back unchanged so a save never drops them.
export function uneditableFunctions(rows: CatalogRow[], functions: Record<string, unknown> | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [name, value] of Object.entries(functions ?? {})) {
    const row = rows.find((r) => r.name === name)
    if (!row || !isSupportedKind(row.kind)) out[name] = value
  }
  return out
}
