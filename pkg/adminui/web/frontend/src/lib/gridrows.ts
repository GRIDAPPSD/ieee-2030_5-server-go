// Wire shapes and pure logic for the timed grid blocks entered on the DERMS
// tab: POST /api/derms/grid-requests (#1156). The page sends the operator's
// direction and an unsigned amount; the server derives the sign, splits the
// amount across the aggregators and decides every conflict. The page words
// the sign it expects in the confirm step, and shows each refusal on the row
// the server names.
import { postJSON, type RequestOptions } from './api'
import { localDateTimeInput, localDateTimeSeconds } from './dercontrol'
import { deviceLabel } from './devices'
import { formatQuantity, scaledNumber, type ScaledValue } from './flowreservation'
import { isObject } from './json'

export const MAX_GRID_ROWS = 24
export const MIN_DURATION_MINUTES = 1
export const MAX_DURATION_MINUTES = 24 * 60
const MAX_AMOUNT = 999_999_999

export type GridDirection = 'discharge' | 'charge'

// Everything the page says about a direction, in one place: the word, where
// the power goes, and the sign opModTargetW carries (discharge positive).
const DIRECTION: Record<GridDirection, { word: string; toFrom: string; sign: '+' | '-'; meaning: string }> = {
  discharge: { word: 'DISCHARGE', toFrom: 'TO the grid', sign: '+', meaning: 'positive = discharge' },
  charge: { word: 'CHARGE', toFrom: 'FROM the grid', sign: '-', meaning: 'negative = charge' },
}

function isDirection(d: string): d is GridDirection {
  return d === 'discharge' || d === 'charge'
}

// directionWord is the upper-case word for a direction as the server names
// it, and any other word as given.
export function directionWord(direction: string): string {
  return isDirection(direction) ? DIRECTION[direction].word : direction
}

// One block as typed. start is a datetime-local value and is read only when
// neither startNow nor follows is set; follows chains the block to the end of
// the row before it, which the server resolves.
export interface GridRowDraft {
  direction: GridDirection | ''
  x: string
  y: string
  durationMinutes: string
  startNow: boolean
  start: string
  follows: boolean
}

export interface GridRowBody {
  direction: GridDirection
  x: number
  y: number
  durationSeconds: number
  start?: number
  followsPrevious?: boolean
}

export interface GridRequestBody {
  rows: GridRowBody[]
}

export interface GridRowError {
  // Numbered from 1, as the page shows it and the server answers it.
  row: number
  message: string
}

// One line of the confirm step. The direction word leads it, so a batch of
// both directions cannot be read as one.
export interface GridConfirmLine {
  row: number
  direction: GridDirection
  text: string
}

export function newRow(): GridRowDraft {
  return { direction: '', x: '', y: '3', durationMinutes: '', startNow: true, start: '', follows: false }
}

// addRow appends a block chained to the end of the last one, with the same
// amount and duration. The direction is left blank: it is never defaulted.
export function addRow(rows: GridRowDraft[]): GridRowDraft[] {
  if (rows.length >= MAX_GRID_ROWS) return rows
  const last = rows[rows.length - 1]
  return [...rows, { ...last, direction: '', startNow: false, start: '', follows: true }]
}

// removeRow drops block n. The first block cannot follow anything, so a block
// that becomes first starts now.
export function removeRow(rows: GridRowDraft[], n: number): GridRowDraft[] {
  const next = rows.filter((_, i) => i !== n)
  if (next.length > 0 && next[0].follows) next[0] = { ...next[0], follows: false, startNow: true, start: '' }
  return next
}

function minutes(r: GridRowDraft): number | null {
  if (!/^\d+$/.test(r.durationMinutes)) return null
  const m = Number(r.durationMinutes)
  return m >= MIN_DURATION_MINUTES && m <= MAX_DURATION_MINUTES ? m : null
}

// rowStartMs is where block i starts on the browser's clock: its typed start,
// the end of the block before it, or anchorMs for a block that starts now. It
// is only what the picker shows; the server resolves a chained start itself.
export function rowStartMs(rows: GridRowDraft[], i: number, anchorMs: number): number | null {
  const r = rows[i]
  if (r.follows && i > 0) {
    const prev = rowStartMs(rows, i - 1, anchorMs)
    const m = minutes(rows[i - 1])
    return prev === null || m === null ? null : prev + m * 60_000
  }
  if (r.startNow || r.follows) return anchorMs
  const s = localDateTimeSeconds(r.start)
  return s === null ? null : s * 1000
}

// pickerValue is what block i's start picker shows: a chained block shows the
// previous end, others what was typed.
export function pickerValue(rows: GridRowDraft[], i: number, anchorMs: number): string {
  const r = rows[i]
  if (!r.follows) return r.start
  const ms = rowStartMs(rows, i, anchorMs)
  return ms === null ? '' : localDateTimeInput(new Date(ms))
}

function amountError(text: string): string {
  if (text.trim() === '') return 'Enter the amount.'
  if (/[+-]/.test(text)) return 'The amount takes no sign. Choose the direction instead.'
  if (!/^\d+$/.test(text)) return 'The amount is a whole number.'
  const n = Number(text)
  return n >= 1 && n <= MAX_AMOUNT ? '' : `The amount is 1 to ${MAX_AMOUNT.toLocaleString('en-US')}.`
}

function multiplierOf(text: string): number | null {
  if (!/^-?\d$/.test(text)) return null
  return Number(text)
}

// rowFigureW is the block's amount in watts, unsigned, or null while the
// amount or multiplier is not valid.
export function rowFigureW(r: GridRowDraft): number | null {
  const y = multiplierOf(r.y)
  return amountError(r.x) === '' && y !== null ? Number(r.x) * Math.pow(10, y) : null
}

// rowErrors lists what keeps block i from being sent, in the order the
// fields appear.
export function rowErrors(r: GridRowDraft, i: number): string[] {
  const out: string[] = []
  if (r.direction === '') out.push('Choose a direction: to the grid or from the grid.')
  const amount = amountError(r.x)
  if (amount !== '') out.push(amount)
  if (multiplierOf(r.y) === null) out.push('The multiplier is a whole number from -9 to 9.')
  if (minutes(r) === null) out.push(`Duration is ${MIN_DURATION_MINUTES} to ${MAX_DURATION_MINUTES} minutes.`)
  if (!r.follows && !r.startNow && localDateTimeSeconds(r.start) === null) out.push('Pick a valid start time.')
  if (r.follows && i === 0) out.push('The first block cannot follow another.')
  return out
}

export function signedWire(direction: GridDirection, x: number, y: number): string {
  const d = DIRECTION[direction]
  return `opModTargetW = ${d.sign}${x} x 10^${y} W (${d.meaning})`
}

function whenText(rows: GridRowDraft[], i: number, minutesLong: number): string {
  const r = rows[i]
  if (r.follows) return `starts when block ${i} ends, for ${minutesLong} min`
  if (r.startNow) return `starts now, for ${minutesLong} min`
  const start = localDateTimeSeconds(r.start) as number
  const end = new Date((start + minutesLong * 60) * 1000)
  const stamp = (d: Date) => localDateTimeInput(d).replace('T', ' ')
  return `from ${stamp(new Date(start * 1000))} to ${stamp(end)} local`
}

export type BuiltGrid =
  | { ok: true; body: GridRequestBody; lines: GridConfirmLine[] }
  | { ok: false; errors: GridRowError[] }

// buildGridRequest turns the typed blocks into the request body and the
// confirm lines, or lists every block that cannot be sent. Nothing is built
// from a partly valid batch.
export function buildGridRequest(rows: GridRowDraft[]): BuiltGrid {
  const errors: GridRowError[] = []
  rows.forEach((r, i) => {
    for (const message of rowErrors(r, i)) errors.push({ row: i + 1, message })
  })
  if (errors.length > 0) return { ok: false, errors }
  const body: GridRequestBody = { rows: [] }
  const lines: GridConfirmLine[] = []
  rows.forEach((r, i) => {
    const direction = r.direction as GridDirection
    const x = Number(r.x)
    const y = multiplierOf(r.y) as number
    const m = minutes(r) as number
    const row: GridRowBody = { direction, x, y, durationSeconds: m * 60 }
    if (r.follows) row.followsPrevious = true
    else if (!r.startNow) row.start = localDateTimeSeconds(r.start) as number
    body.rows.push(row)
    lines.push({
      row: i + 1,
      direction,
      text:
        `Block ${i + 1}: ${DIRECTION[direction].word} ${formatQuantity(x * Math.pow(10, y), 'W')} ${DIRECTION[direction].toFrom}, ${whenText(rows, i, m)}. ` +
        `Wire, batch total: ${signedWire(direction, x, y)}.`,
    })
  })
  return { ok: true, body, lines }
}

export interface GridConflict {
  row: number
  batch?: string
  start?: number
  end?: number
}

export interface GridRowRefusal {
  row: number
  code: string
  conflictsWith?: GridConflict
  aggregatorLFDI?: string
}

export interface GridDeviceEvents {
  device: string
  aggregatorLFDI: string
  live: number
  added: number
}

export interface GridAggregatorRefusal {
  aggregatorLFDI: string
  device: string
  code: string
}

export interface GridRefusal {
  code: string
  error: string
  rows: GridRowRefusal[]
  devices: GridDeviceEvents[]
  aggregators: GridAggregatorRefusal[]
}

// parseGridRefusal reads the 409 and 422 body, or null when it is not one.
export function parseGridRefusal(body: unknown): GridRefusal | null {
  if (!isObject(body) || typeof body.code !== 'string' || typeof body.error !== 'string') return null
  const rows: GridRowRefusal[] = []
  for (const r of Array.isArray(body.rows) ? body.rows : []) {
    if (!isObject(r) || typeof r.row !== 'number' || typeof r.code !== 'string') return null
    const refusal: GridRowRefusal = { row: r.row, code: r.code }
    if (isObject(r.conflictsWith) && typeof r.conflictsWith.row === 'number') refusal.conflictsWith = r.conflictsWith as unknown as GridConflict
    if (typeof r.aggregatorLFDI === 'string') refusal.aggregatorLFDI = r.aggregatorLFDI
    rows.push(refusal)
  }
  const devices = (Array.isArray(body.devices) ? body.devices : []).filter(
    (d): d is GridDeviceEvents => isObject(d) && typeof d.device === 'string' && typeof d.live === 'number' && typeof d.added === 'number',
  )
  const aggregators = (Array.isArray(body.aggregators) ? body.aggregators : []).filter(
    (a): a is GridAggregatorRefusal =>
      isObject(a) && typeof a.aggregatorLFDI === 'string' && typeof a.device === 'string' && typeof a.code === 'string',
  )
  return { code: body.code, error: body.error, rows, devices, aggregators }
}

const ROW_TEXT: Record<string, string> = {
  direction_required: 'Choose a direction.',
  direction_unknown: 'The direction is not one the server knows.',
  x_required: 'Enter the amount.',
  x_negative: 'The amount takes no sign. Choose the direction instead.',
  x_range: 'The amount is out of range.',
  y_required: 'Enter the multiplier.',
  y_range: 'The multiplier is out of range.',
  duration_required: 'Enter the duration.',
  duration_out_of_range: 'The duration is out of range.',
  start_in_past: 'The start is in the past.',
  start_and_follows_previous: 'The block has both a start and a chain to the block before it.',
  follows_previous_first_row: 'The first block cannot follow another.',
  follows_invalid: 'It follows a block that was refused.',
  too_many_events: 'It would take a device past 24 live controls.',
  share_out_of_range: 'A share of the amount does not fit the wire.',
  invalid: 'The server found the block invalid.',
}

function conflictText(c: GridConflict | undefined): string {
  if (c === undefined) return 'It overlaps another block.'
  if (c.batch === undefined) return `It overlaps block ${c.row} of this batch.`
  const window = c.start === undefined || c.end === undefined ? '' : `, ${new Date(c.start * 1000).toISOString()} to ${new Date(c.end * 1000).toISOString()}`
  return `It overlaps a live block of batch ${c.batch} (its row ${c.row}${window}).`
}

// unavailableText names the aggregator a row's share could not reach and the
// device of its fleet the server names, from the refusal's aggregators list.
function unavailableText(r: GridRowRefusal, aggregators: GridAggregatorRefusal[]): string {
  if (r.aggregatorLFDI === undefined) return "An aggregator's fleet cannot take it."
  const name = deviceLabel(r.aggregatorLFDI)
  const named = aggregators.filter((a) => a.aggregatorLFDI === r.aggregatorLFDI)
  if (named.length === 0) return `Aggregator ${name} cannot take it.`
  return `Aggregator ${name} cannot take it: ${named.map((a) => `device ${a.device} (${a.code})`).join(', ')}.`
}

// refusalText words one refused row, always ending in the server's code.
export function refusalText(r: GridRowRefusal, aggregators: GridAggregatorRefusal[] = []): string {
  const text =
    r.code === 'overlapping_request'
      ? conflictText(r.conflictsWith)
      : r.code === 'aggregator_unavailable'
        ? unavailableText(r, aggregators)
        : (ROW_TEXT[r.code] ?? 'The server refused it.')
  return `${text} (${r.code})`
}

// refusedRows groups a refusal's row messages by block number.
export function refusedRows(refusal: GridRefusal): Map<number, string[]> {
  const out = new Map<number, string[]>()
  for (const r of refusal.rows) out.set(r.row, [...(out.get(r.row) ?? []), refusalText(r, refusal.aggregators)])
  return out
}

export interface GridShare {
  aggregatorLFDI: string
  devices: number
  targetW: ScaledValue | null
  issued: boolean
}

export interface GridRowResult {
  row: number
  direction: string
  requestedW: number
  encodedW: number
  differenceW: number
  shares: GridShare[]
}

export interface GridCreated {
  batch: string
  rows: GridRowResult[]
  warnings: string[]
  persisted: boolean
}

function readShare(s: unknown): GridShare | null {
  if (!isObject(s) || typeof s.aggregatorLFDI !== 'string' || typeof s.devices !== 'number' || typeof s.issued !== 'boolean') return null
  const t = s.targetW
  const target = isObject(t) && typeof t.value === 'number' && typeof t.multiplier === 'number' ? (t as unknown as ScaledValue) : null
  return { aggregatorLFDI: s.aggregatorLFDI, devices: s.devices, targetW: target, issued: s.issued }
}

// parseGridCreated reads the 201 body, or null when any figure the page shows
// is missing.
export function parseGridCreated(data: unknown): GridCreated | null {
  if (!isObject(data) || typeof data.batch !== 'string' || !Array.isArray(data.rows) || typeof data.persisted !== 'boolean') return null
  const rows: GridRowResult[] = []
  for (const r of data.rows) {
    if (!isObject(r) || typeof r.row !== 'number' || typeof r.direction !== 'string' || !Array.isArray(r.shares)) return null
    if (typeof r.requestedW !== 'number' || typeof r.encodedW !== 'number' || typeof r.differenceW !== 'number') return null
    const shares = r.shares.map(readShare)
    if (shares.some((s) => s === null)) return null
    rows.push({ row: r.row, direction: r.direction, requestedW: r.requestedW, encodedW: r.encodedW, differenceW: r.differenceW, shares: shares as GridShare[] })
  }
  const warnings = (Array.isArray(data.warnings) ? data.warnings : []).filter((w): w is string => typeof w === 'string')
  return { batch: data.batch, rows, warnings, persisted: data.persisted }
}

// shareText words one aggregator's share: the signed figure it carries, or
// that it was below the wire's resolution and not issued.
export function shareText(s: GridShare): string {
  if (!s.issued) return `${s.devices} devices: below resolution, not issued`
  const w = scaledNumber(s.targetW)
  return `${s.devices} devices: ${w === null ? 'missing' : formatQuantity(w, 'W')} (opModTargetW)`
}

export type GridResult =
  | { ok: true; created: GridCreated }
  | { ok: false; kind: 'refused'; message: string; refusal: GridRefusal | null }
  | { ok: false; kind: 'unknown'; message: string }

// keptShares words the 500 body that names the shares an undo could not
// remove, since any of them may be live.
function keptText(body: unknown): string | null {
  if (!isObject(body) || !Array.isArray(body.kept) || typeof body.batch !== 'string') return null
  const named = body.kept
    .filter((k): k is Record<string, unknown> => isObject(k) && typeof k.mRID === 'string')
    .map((k) => `${k.mRID}${typeof k.cancelHref === 'string' ? ' (' + k.cancelHref + ')' : ''}`)
  return `Batch ${body.batch} was partly kept and may be live: ${named.join(', ')}. Cancel what is not wanted.`
}

// submitGridRequest posts the batch. A refusal is the server's answer to show
// as given; no answer or a 5xx is an outcome the page cannot know, so the
// batch is never re-sent on a guess.
export async function submitGridRequest(body: GridRequestBody, opts: RequestOptions): Promise<GridResult> {
  const res = await postJSON<unknown>('/api/derms/grid-requests', body, opts)
  if (!res.ok) {
    const kept = keptText(res.body)
    if (kept !== null) return { ok: false, kind: 'unknown', message: kept }
    if (res.status === 0 || res.status >= 500) {
      const said = res.status === 0 ? `No answer from the server (${res.error})` : `The server answered ${res.status}: ${res.error}`
      return { ok: false, kind: 'unknown', message: `${said}. Outcome unknown: the batch may have been written. Check the Controls tab before sending again.` }
    }
    const refusal = parseGridRefusal(res.body)
    return { ok: false, kind: 'refused', message: `The server refused the batch: ${res.error}${refusal === null ? '' : ' (' + refusal.code + ')'}.`, refusal }
  }
  const created = parseGridCreated(res.data)
  if (created === null) {
    return { ok: false, kind: 'unknown', message: 'The server accepted the batch but its reply could not be read. Check the Controls tab before sending again.' }
  }
  return { ok: true, created }
}
