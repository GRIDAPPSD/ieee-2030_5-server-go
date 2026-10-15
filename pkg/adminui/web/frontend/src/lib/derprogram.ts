// Wire shapes, form validation and requests for DER programs: the FSA
// templates (internal/handler/admin_derprogram.go, admin_derprogram_edit.go)
// and the programs on one device, which take the same body, plus the flat
// list of every program (admin_derprogram_list.go). The FSAs tab and the
// device Programs drawer both build on this file.

import { deleteJSON, fetchJSON, postJSON, putJSON, type ApiResult } from './api'

export type ControlKind = 'connect' | 'disconnect' | 'maxLimW' | 'fixedPFInjectW' | 'targetW'

export const CONTROL_KINDS: { kind: ControlKind; label: string }[] = [
  { kind: 'connect', label: 'Connect' },
  { kind: 'disconnect', label: 'Disconnect' },
  { kind: 'maxLimW', label: 'Max power limit (hundredths of a percent of setMaxW)' },
  { kind: 'fixedPFInjectW', label: 'Fixed power factor' },
  { kind: 'targetW', label: 'Target power' },
]

export interface DefaultControlBody {
  type: ControlKind
  maxLimW?: number
  powerFactor?: { displacement: number; excitation: boolean }
  targetW?: { value: number; multiplier?: number }
}

export interface ProgramBody {
  description: string
  primacy: number
  mRID?: string
  defaultControl?: DefaultControlBody
}

export interface DefaultControlView {
  mRID: string
  type: string
  derControlBase: {
    opModConnect?: boolean
    opModEnergize?: boolean
    opModMaxLimW?: number
    opModFixedPFInjectW?: { displacement: number; excitation: boolean; multiplier: number }
    opModTargetW?: { value: number; multiplier: number }
  }
}

// A device program's defaultControl is absent when it has none.
export interface ProgramView {
  id: string
  mRID: string
  description: string
  primacy: number
  version: number
  defaultControl?: DefaultControlView
}

export interface ProgramTemplate extends ProgramView {
  defaultControl: DefaultControlView
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
  controlType: ControlKind | ''
  maxLimW: string
  displacement: string
  excitation: 'false' | 'true'
  targetValue: string
  targetMultiplier: string
}

export function emptyForm(): ProgramFormValues {
  return {
    description: '',
    primacy: '',
    mRID: '',
    controlType: '',
    maxLimW: '',
    displacement: '',
    excitation: 'false',
    targetValue: '',
    targetMultiplier: '',
  }
}

// formFromProgram prefills an edit form from a stored program. The mRID
// field stays blank: the server refuses a changed mRID, so an edit never
// sends one and the stored mRID is shown beside the form instead.
export function formFromProgram(p: ProgramView): ProgramFormValues {
  const f = { ...emptyForm(), description: p.description, primacy: String(p.primacy) }
  const base = p.defaultControl?.derControlBase
  const type = p.defaultControl?.type
  if (!base || !type) return f
  switch (type) {
    case 'connect':
    case 'disconnect':
      f.controlType = type
      break
    case 'maxLimW':
      f.controlType = type
      f.maxLimW = String(base.opModMaxLimW ?? '')
      break
    case 'fixedPFInjectW':
      f.controlType = type
      f.displacement = String(base.opModFixedPFInjectW?.displacement ?? '')
      f.excitation = base.opModFixedPFInjectW?.excitation ? 'true' : 'false'
      break
    case 'targetW':
      f.controlType = type
      f.targetValue = String(base.opModTargetW?.value ?? '')
      f.targetMultiplier = String(base.opModTargetW?.multiplier ?? '')
      break
  }
  return f
}

const WHOLE_NUMBER = /^-?[0-9]+$/
const MRID = /^[0-9a-fA-F]{32}$/

function wholeNumber(s: string): number | undefined {
  const t = s.trim()
  return WHOLE_NUMBER.test(t) ? Number(t) : undefined
}

export type BuiltBody = { ok: true; body: ProgramBody } | { ok: false; error: string }

// buildProgramBody validates the form the way the server does and words each
// refusal the way it would, so the operator sees one message whether the
// check ran here or there. requireDefault is true on create, where the
// server demands a default control, and false on an edit, where leaving the
// type blank keeps the stored control.
export function buildProgramBody(v: ProgramFormValues, opts: { requireDefault: boolean }): BuiltBody {
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

  if (v.controlType === '') {
    return opts.requireDefault ? { ok: false, error: 'defaultControl.type: required' } : { ok: true, body }
  }
  switch (v.controlType) {
    case 'connect':
    case 'disconnect':
      body.defaultControl = { type: v.controlType }
      break
    case 'maxLimW': {
      const n = wholeNumber(v.maxLimW)
      if (v.maxLimW.trim() === '') return { ok: false, error: 'defaultControl.maxLimW: required for type maxLimW' }
      if (n === undefined || n < 0 || n > 10000) {
        return { ok: false, error: 'defaultControl.maxLimW: must be 0 to 10000' }
      }
      body.defaultControl = { type: 'maxLimW', maxLimW: n }
      break
    }
    case 'fixedPFInjectW': {
      const n = wholeNumber(v.displacement)
      if (v.displacement.trim() === '') {
        return { ok: false, error: 'defaultControl.powerFactor.displacement: required' }
      }
      if (n === undefined || n < 1 || n > 1000) {
        return { ok: false, error: 'defaultControl.powerFactor.displacement: must be 1 to 1000' }
      }
      body.defaultControl = {
        type: 'fixedPFInjectW',
        powerFactor: { displacement: n, excitation: v.excitation === 'true' },
      }
      break
    }
    case 'targetW': {
      const n = wholeNumber(v.targetValue)
      if (v.targetValue.trim() === '') return { ok: false, error: 'defaultControl.targetW.value: required' }
      if (n === undefined || n < -32768 || n > 32767) {
        return { ok: false, error: 'defaultControl.targetW.value: must be -32768 to 32767' }
      }
      const targetW: { value: number; multiplier?: number } = { value: n }
      if (v.targetMultiplier.trim() !== '') {
        const m = wholeNumber(v.targetMultiplier)
        if (m === undefined || m < -9 || m > 9) {
          return { ok: false, error: 'defaultControl.targetW.multiplier: must be -9 to 9' }
        }
        targetW.multiplier = m
      }
      body.defaultControl = { type: 'targetW', targetW }
      break
    }
  }
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
