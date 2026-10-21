// Wire shapes and requests for a control sent to an FSA's program template
// (internal/handler/admin_fsa_control.go): a scheduled control goes to the
// FSA control route and reaches every device assigned to the FSA, and a
// default saves through the template's edit route. The form that builds the
// values is the device form (controlcatalog.ts); this file only addresses it
// to an FSA.

import { fetchJSON, postJSON, putJSON, type ApiResult } from './api'
import type { DeviceControlBody } from './controlcatalog'
import type { ProgramTemplate } from './derprogram'

export interface FsaControlCopy {
  device: string
  href: string
  // null when the device holds no copy.
  eventStatus: { currentStatus: number; status: string; dateTime: number } | null
}

export interface FsaControlView {
  id: string
  mRID: string
  type: string
  description: string
  derControlBase: Record<string, unknown>
  functions?: Record<string, unknown>
  creationTime: number
  interval: { start: number; duration: number }
  ended: boolean
  cancelled: boolean
  cancelledAt: number | null
  cancelReason?: string
  copies: FsaControlCopy[]
}

export interface FsaControlCreated extends FsaControlView {
  notificationAttempted: boolean
  persisted: boolean
}

export interface FsaControlList {
  fsa: string
  program: string
  controls: FsaControlView[] | null
}

const enc = encodeURIComponent

function controlsPath(fsaId: string, templateId: string): string {
  return `/api/fsas/${enc(fsaId)}/der-programs/${enc(templateId)}/controls`
}

// fsaControlRequest is the create body: the device body without `kind`,
// which the FSA route refuses as an unknown member.
export function fsaControlRequest(body: DeviceControlBody): Omit<DeviceControlBody, 'kind'> {
  const { kind: _kind, ...rest } = body
  return rest
}

export function listFsaControls(fsaId: string, templateId: string): Promise<ApiResult<FsaControlList>> {
  return fetchJSON(controlsPath(fsaId, templateId))
}

export function createFsaControl(fsaId: string, templateId: string, body: DeviceControlBody): Promise<ApiResult<FsaControlCreated>> {
  return postJSON(controlsPath(fsaId, templateId), fsaControlRequest(body))
}

export function cancelFsaControl(fsaId: string, templateId: string, controlId: string, reason: string): Promise<ApiResult<FsaControlView>> {
  return postJSON(`${controlsPath(fsaId, templateId)}/${enc(controlId)}/cancel`, reason ? { reason } : {})
}

// saveFsaDefault replaces the template's default control with the functions
// set. The edit route takes the whole program body, so the stored
// description and primacy are sent back unchanged.
export function saveFsaDefault(
  fsaId: string,
  template: Pick<ProgramTemplate, 'id' | 'description' | 'primacy'>,
  functions: Record<string, unknown>,
): Promise<ApiResult<ProgramTemplate>> {
  return putJSON(`/api/fsas/${enc(fsaId)}/der-programs/${enc(template.id)}`, {
    description: template.description,
    primacy: template.primacy,
    defaultControl: { functions },
  })
}

// devicesHolding counts the copies a device actually holds; a copy with no
// event status is a device that was assigned but holds nothing.
export function devicesHolding(c: Pick<FsaControlView, 'copies'>): number {
  return c.copies.filter((x) => x.eventStatus !== null).length
}

export function holdingText(c: Pick<FsaControlView, 'copies'>): string {
  return `${devicesHolding(c)} of ${c.copies.length} devices`
}

// fsaControlStatus words the record's state. Cancelled and ended come from
// the record; otherwise the distinct statuses of the copies are listed, so a
// control one device has started and another has not shows both.
export function fsaControlStatus(c: FsaControlView): string {
  if (c.cancelled) return 'cancelled'
  if (c.ended) return 'ended'
  const seen = new Set<string>()
  for (const x of c.copies) if (x.eventStatus) seen.add(x.eventStatus.status)
  return seen.size === 0 ? 'no device holds it' : [...seen].join(', ')
}

// canCancelFsaControl: the server refuses a cancel of a cancelled or ended
// record, so only a live record offers it.
export function canCancelFsaControl(c: Pick<FsaControlView, 'cancelled' | 'ended'>): boolean {
  return !c.cancelled && !c.ended
}
