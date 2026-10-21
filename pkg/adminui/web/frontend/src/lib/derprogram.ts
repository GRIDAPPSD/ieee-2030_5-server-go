// Wire shapes, form validation and requests for DER programs: the FSA
// templates (internal/handler/admin_derprogram.go, admin_derprogram_edit.go)
// and the programs on one device, which take the same body, plus the flat
// list of every program (admin_derprogram_list.go). The FSAs tab and the
// device Programs drawer both build on this file.

import { deleteJSON, fetchJSON, postJSON, putJSON, type ApiResult } from './api'

// A program's default control as the program routes take it: the catalog's
// functions with their values, the same `functions` member a control sends.
export interface DefaultControlBody {
  functions: Record<string, unknown>
}

export interface ProgramBody {
  description: string
  primacy: number
  mRID?: string
  defaultControl?: DefaultControlBody | null
}

export interface DefaultControlView {
  mRID: string
  type: string
  // The default's functions in the request shape, as a control form sends
  // them, so the form reads a default back without naming control elements.
  functions: Record<string, unknown>
  derControlBase: Record<string, unknown>
}

// A program's defaultControl is absent when it has none: a device program,
// or a template whose default was removed.
export interface ProgramView {
  id: string
  mRID: string
  description: string
  primacy: number
  version: number
  defaultControl?: DefaultControlView
}

export interface ProgramTemplate extends ProgramView {
  copies: string[]
}

export interface DeviceProgram extends ProgramView {
  href: string
}

export interface ProgramListItem {
  device: string
  fsa: string
  id: string
  href: string
  mRID: string
  description: string
  primacy: number
  version: number
  origin: string
  templateId?: string
  onRestart: string
}

export interface ProgramCatalog {
  persists: boolean
  programs: ProgramListItem[]
}

// The server caps a template count per FSA at this; shown so the Add form
// can say why it is refused before the request is sent.
export const MAX_TEMPLATES_PER_FSA = 16

const PAGE_LIMIT = 500
// Pages are bounded so a server whose total never converges cannot spin the
// panel: 100 pages is 50000 programs.
const MAX_PAGES = 100

export interface Refusal {
  status: number
  message: string
  code: string | undefined
  controls: string[]
}

type Failure = Extract<ApiResult<unknown>, { ok: false }>

// refusalOf reads a failed call as the server sent it: its message, and for
// a 409 the machine code and the live controls that blocked a delete.
export function refusalOf(res: Failure): Refusal {
  const body = res.body && typeof res.body === 'object' ? (res.body as Record<string, unknown>) : {}
  const controls = Array.isArray(body.controls) ? body.controls.filter((c): c is string => typeof c === 'string') : []
  return {
    status: res.status,
    message: res.error,
    code: typeof body.code === 'string' ? body.code : undefined,
    controls,
  }
}

export interface ProgramFormValues {
  description: string
  primacy: string
  mRID: string
}

export function emptyForm(): ProgramFormValues {
  return { description: '', primacy: '', mRID: '' }
}

// formFromProgram prefills an edit form from a stored program. The mRID
// field stays blank: the server refuses a changed mRID, so an edit never
// sends one and the stored mRID is shown beside the form instead. The
// default control's fields are filled by the form from the catalog.
export function formFromProgram(p: ProgramView): ProgramFormValues {
  return { ...emptyForm(), description: p.description, primacy: String(p.primacy) }
}

const WHOLE_NUMBER = /^-?[0-9]+$/
const MRID = /^[0-9a-fA-F]{32}$/

function wholeNumber(s: string): number | undefined {
  const t = s.trim()
  return WHOLE_NUMBER.test(t) ? Number(t) : undefined
}

const NO_FUNCTION = 'defaultControl: tick at least one function'

export type BuiltBody = { ok: true; body: ProgramBody } | { ok: false; error: string }

// The default control as the form's ticked fields built it: the functions,
// the reason they could not be built, or null when nothing is ticked.
export type DefaultControlInput = { ok: true; functions: Record<string, unknown> } | { ok: false; error: string } | null

// buildProgramBody validates the form the way the server does and words each
// refusal the way it would, so the operator sees one message whether the
// check ran here or there. requireDefault is true on create, where the
// server demands a default control. clearDefault, on an edit, sends
// "defaultControl": null to remove the stored default; with a null
// defaultControl and no clearDefault the body omits the key and the stored
// default stays.
export function buildProgramBody(
  v: ProgramFormValues,
  opts: { requireDefault: boolean; defaultControl: DefaultControlInput; clearDefault?: boolean },
): BuiltBody {
  const description = v.description
  if (description.length < 1 || description.length > 32 || !/^[\x20-\x7e]+$/.test(description)) {
    return { ok: false, error: 'description: 1 to 32 printable ASCII characters required' }
  }
  const primacy = wholeNumber(v.primacy)
  if (primacy === undefined || !((primacy >= 0 && primacy <= 2) || (primacy >= 65 && primacy <= 191))) {
    return { ok: false, error: 'primacy: must be 0, 1, 2 or 65 to 191' }
  }
  const body: ProgramBody = { description, primacy }

  const mRID = v.mRID.trim()
  if (mRID !== '') {
    if (!MRID.test(mRID) || mRID.toUpperCase().startsWith('FFFFFFFFFFFFFFFFFFFFFFFF')) {
      return { ok: false, error: 'mRID: must be 32 hex digits, not the reserved form whose first 24 digits are F' }
    }
    body.mRID = mRID
  }

  if (opts.clearDefault) {
    body.defaultControl = null
    return { ok: true, body }
  }
  const control = opts.defaultControl
  if (control === null) {
    return opts.requireDefault ? { ok: false, error: NO_FUNCTION } : { ok: true, body }
  }
  if (!control.ok) return { ok: false, error: control.error }
  if (Object.keys(control.functions).length === 0) return { ok: false, error: NO_FUNCTION }
  body.defaultControl = { functions: control.functions }
  return { ok: true, body }
}

const enc = encodeURIComponent

export function listTemplates(fsaId: string): Promise<ApiResult<{ templates: ProgramTemplate[] }>> {
  return fetchJSON(`/api/fsas/${enc(fsaId)}/der-programs`)
}

export function createTemplate(fsaId: string, body: ProgramBody): Promise<ApiResult<ProgramTemplate>> {
  return postJSON(`/api/fsas/${enc(fsaId)}/der-programs`, body)
}

export function updateTemplate(fsaId: string, id: string, body: ProgramBody): Promise<ApiResult<ProgramTemplate>> {
  return putJSON(`/api/fsas/${enc(fsaId)}/der-programs/${enc(id)}`, body)
}

export function deleteTemplate(fsaId: string, id: string): Promise<ApiResult<void>> {
  return deleteJSON(`/api/fsas/${enc(fsaId)}/der-programs/${enc(id)}`)
}

export function createDeviceProgram(deviceId: string, fsaId: string, body: ProgramBody): Promise<ApiResult<DeviceProgram>> {
  return postJSON(`/api/devices/${enc(deviceId)}/fsas/${enc(fsaId)}/der-programs`, body)
}

export function updateDeviceProgram(deviceId: string, id: string, body: ProgramBody): Promise<ApiResult<DeviceProgram>> {
  return putJSON(`/api/devices/${enc(deviceId)}/der-programs/${enc(id)}`, body)
}

export function deleteDeviceProgram(deviceId: string, id: string): Promise<ApiResult<void>> {
  return deleteJSON(`/api/devices/${enc(deviceId)}/der-programs/${enc(id)}`)
}

// listAllPrograms reads GET /api/der-programs page by page until every
// program is in. An empty page ends the walk even when total says more, and
// a reply without a programs array is an error, not an empty catalog.
export async function listAllPrograms(opts?: { signal?: AbortSignal }): Promise<ApiResult<ProgramCatalog>> {
  const programs: ProgramListItem[] = []
  let persists = false
  for (let page = 0; page < MAX_PAGES; page++) {
    const res = await fetchJSON<{
      persists: boolean
      total: number
      programs: ProgramListItem[]
    }>(`/api/der-programs?limit=${PAGE_LIMIT}&start=${programs.length}`, opts)
    if (!res.ok) return res
    if (!Array.isArray(res.data?.programs)) {
      return { ok: false, error: 'the program list reply has no programs list', status: 200 }
    }
    persists = res.data.persists === true
    programs.push(...res.data.programs)
    if (res.data.programs.length === 0 || programs.length >= res.data.total) break
  }
  return { ok: true, data: { persists, programs } }
}

// A program's origin, as GET /api/der-programs reports it. A template copy
// is owned by its FSA's template and is edited there; the other two are
// edited on the device.
export function isDeviceEditable(p: Pick<ProgramListItem, 'origin'>): boolean {
  return p.origin === 'device' || p.origin === 'fixture'
}

const ORIGIN_LABELS: Record<string, string> = {
  template: 'Template copy',
  fixture: 'Fixture',
  device: 'Device',
}

const RESTART_LABELS: Record<string, string> = {
  kept: 'Kept on restart',
  reseeded: 'Reseeded on restart',
  gone: 'Lost on restart',
}

// An origin or onRestart value this build does not know is shown as the
// server sent it, never as a guess.
export function originLabel(origin: string): string {
  return ORIGIN_LABELS[origin] ?? origin
}

export function restartLabel(onRestart: string): string {
  return RESTART_LABELS[onRestart] ?? onRestart
}

export interface FsaGroup {
  fsa: string
  programs: ProgramListItem[]
}

// groupByFsa takes one device's programs from the whole catalog and groups
// them by FSA, in first-seen order. assignedFsas adds the FSAs the device is
// assigned to that hold no program yet, so a device program can be added
// there.
export function groupByFsa(items: ProgramListItem[], deviceId: string, assignedFsas: string[]): FsaGroup[] {
  const groups = new Map<string, ProgramListItem[]>()
  for (const p of items) {
    if (p.device !== deviceId) continue
    const list = groups.get(p.fsa)
    if (list) list.push(p)
    else groups.set(p.fsa, [p])
  }
  for (const fsa of assignedFsas) {
    if (!groups.has(fsa)) groups.set(fsa, [])
  }
  return [...groups].map(([fsa, programs]) => ({ fsa, programs }))
}
