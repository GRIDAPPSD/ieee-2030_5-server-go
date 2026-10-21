// Wire shapes and display helpers for the admin DER control API
// (internal/handler/admin_dercontrol.go), kept apart from the panel so they
// are testable without mounting a component. The create request is built in
// controlcatalog.ts.

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

export interface DERControlView {
  mRID: string
  href: string
  derProgramHref: string
  derControlListHref: string
  type: string
  description: string
  // Keyed by the control's own element names; read only as data.
  derControlBase: Record<string, unknown>
  // The control's functions in the shapes a create request takes.
  functions?: Record<string, unknown>
  ended?: boolean
  creationTime: number
  interval: { start: number; duration: number }
  eventStatus: { currentStatus: number; status: string; dateTime: number }
  executesGrant?: string | null
}

// The 201 body of POST /api/devices/{id}/controls: exactly one of control
// (kind scheduled) and defaultControl (kind default) is set.
export interface DeviceControlCreated {
  kind: string
  fsa: string
  program: string
  programHref: string
  primacy: number
  primacyReason: string
  control?: DERControlCreated
  defaultControl?: { mRID: string; type: string; derControlBase: Record<string, unknown> }
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

// Metered export over the control's effective window (Unix seconds).
// deliveredWh and averageW (watts over the covered seconds) are null when no
// reading covers any second of it. readingsExpired is true when retention may
// have removed readings the window needs; the figure is then null.
// concurrentMirrors: two DER mirrors of the
// device covered one second, so the figure may undercount; excludedReadings:
// DER W readings in the window the figure could not read.
export interface DERControlDelivery {
  windowStart: number
  windowEnd: number
  deliveredWh: number | null
  averageW: number | null
  coveredSeconds: number
  readings: number
  directionUnknown: boolean
  concurrentMirrors: boolean
  excludedReadings: number
  deviceLFDI: string
  newestReadingTime: number | null
  readingsExpired: boolean
}

export interface DERControlListItem extends DERControlView {
  responses: DERControlResponseCounts
  delivery?: DERControlDelivery | null
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
