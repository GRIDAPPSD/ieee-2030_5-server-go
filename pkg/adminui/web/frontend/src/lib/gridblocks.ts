// Wire shapes and pure logic for the scheduled grid blocks on the DERMS tab:
// GET /api/derms/grid-requests and the two cancel routes (#1157). The server
// derives every status and flag on each read, so the page shows them as
// given: a null deliveredW is "no reading", never 0, and liveness is the one
// rule the Controls tab uses (canCancel), never a second one here.
import { fetchJSON, postJSON, type RequestOptions } from './api'
import { isControlEventStatus, type ControlEvent } from './controlEvents'
import { canCancel } from './dercontrol'
import { isDirection, type Direction } from './direction'
import { isObject } from './json'

export const GRID_REQUESTS_PATH = '/api/derms/grid-requests'
// The look-ahead the timeline draws, and the look-behind that keeps the "now"
// line off the left edge and a running block drawn from where it started.
export const TIMELINE_AHEAD_SECONDS = 24 * 3600
export const TIMELINE_BEHIND_SECONDS = 3600

export interface GridBlockShare {
  aggregatorLFDI: string
  devices: number
  status: string
  aggregatorGone: boolean
  deliveredW: number | null
  mRID: string
}

export interface GridBlock {
  row: number
  direction: Direction
  requestedW: number | null
  encodedW: number | null
  differenceW: number | null
  // Epoch seconds.
  start: number
  end: number
  durationSeconds: number
  status: string
  basisChanged: boolean
  deliveredW: number | null
  shares: GridBlockShare[]
}

// One block with the batch it belongs to: the unit both views draw and the
// cancel routes name.
export interface BlockEntry {
  batch: string
  block: GridBlock
}

const nullableNumber = (v: unknown): v is number | null => v === null || (typeof v === 'number' && Number.isFinite(v))

function readShare(s: unknown): GridBlockShare | null {
  if (!isObject(s) || typeof s.aggregatorLFDI !== 'string' || typeof s.status !== 'string') return null
  if (typeof s.devices !== 'number' || typeof s.aggregatorGone !== 'boolean' || !nullableNumber(s.deliveredW)) return null
  return {
    aggregatorLFDI: s.aggregatorLFDI,
    devices: s.devices,
    status: s.status,
    aggregatorGone: s.aggregatorGone,
    deliveredW: s.deliveredW,
    mRID: typeof s.mRID === 'string' ? s.mRID : '',
  }
}

function readBlock(r: unknown): GridBlock | null {
  if (!isObject(r) || typeof r.row !== 'number' || !isDirection(r.direction)) return null
  if (typeof r.start !== 'number' || typeof r.end !== 'number' || typeof r.durationSeconds !== 'number' || typeof r.status !== 'string') return null
  if (!nullableNumber(r.requestedW) || !nullableNumber(r.encodedW) || !nullableNumber(r.differenceW) || !nullableNumber(r.deliveredW)) return null
  if (typeof r.basisChanged !== 'boolean' || !Array.isArray(r.shares)) return null
  const shares = r.shares.map(readShare)
  if (shares.some((s) => s === null)) return null
  return {
    row: r.row,
    direction: r.direction,
    requestedW: r.requestedW,
    encodedW: r.encodedW,
    differenceW: r.differenceW,
    start: r.start,
    end: r.end,
    durationSeconds: r.durationSeconds,
    status: r.status,
    basisChanged: r.basisChanged,
    deliveredW: r.deliveredW,
    shares: shares as GridBlockShare[],
  }
}

// parseGridBlocks flattens the list into entries, or returns null when any
// field the page shows has the wrong type, so a malformed reply never draws
// as a partial one.
export function parseGridBlocks(data: unknown): BlockEntry[] | null {
  if (!isObject(data) || !Array.isArray(data.batches)) return null
  const out: BlockEntry[] = []
  for (const b of data.batches) {
    if (!isObject(b) || typeof b.batch !== 'string' || !Array.isArray(b.rows)) return null
    for (const r of b.rows) {
      const block = readBlock(r)
      if (block === null) return null
      out.push({ batch: b.batch, block })
    }
  }
  return sortedByStart(out)
}

// sortedByStart orders blocks by start, then batch and row, so equal starts
// keep a stable order.
export function sortedByStart(entries: BlockEntry[]): BlockEntry[] {
  return [...entries].sort((a, b) => a.block.start - b.block.start || a.batch.localeCompare(b.batch) || a.block.row - b.block.row)
}

// isLiveBlock is true while any share of the block can still be cancelled. It
// asks canCancel of each share, the rule the Cancel button and the Controls
// tab filter use, so a "partly cancelled" or "mixed" block stays listed
// while one of its shares is live.
export function isLiveBlock(e: BlockEntry, serverNowSeconds: number | null): boolean {
  return e.block.shares.some((s) => canCancel(s.status, e.block.start, e.block.durationSeconds, serverNowSeconds))
}

// shownBlocks is the one list both views draw: every block, or only the live
// ones when the filter is on.
export function shownBlocks(entries: BlockEntry[], liveOnly: boolean, serverNowSeconds: number | null): BlockEntry[] {
  return liveOnly ? entries.filter((e) => isLiveBlock(e, serverNowSeconds)) : entries
}

// blockEvent is the block as the Overview timeline's event, so the same bar
// layout and labels draw it. The target is the encoded figure the devices
// receive, falling back to the requested one; a block with neither has none.
export function blockEvent(e: BlockEntry): ControlEvent {
  const { block } = e
  const status = isControlEventStatus(block.status) ? block.status : 'unknown'
  return {
    group: block.direction,
    groupKind: 'aggregator',
    scope: 'fsa',
    deviceIDs: [],
    mrid: `${e.batch}:${block.row}`,
    origin: 'derms',
    direction: block.direction,
    targetW: block.encodedW ?? block.requestedW,
    modes: [],
    status,
    start: block.start,
    end: block.end,
  }
}

export type GridBlocksResult =
  | { ok: true; entries: BlockEntry[]; serverTime: number | null }
  | { ok: false; error: string; absent: boolean }

export async function fetchGridBlocks(opts: RequestOptions): Promise<GridBlocksResult> {
  const res = await fetchJSON<unknown>(GRID_REQUESTS_PATH, opts)
  if (!res.ok) return { ok: false, error: res.error, absent: res.status === 404 }
  const entries = parseGridBlocks(res.data)
  if (entries === null) return { ok: false, error: 'The server sent a grid request reply that could not be read.', absent: false }
  return { ok: true, entries, serverTime: res.serverTime === undefined ? null : Math.floor(res.serverTime / 1000) }
}

export interface FailedShare {
  row: number
  aggregatorLFDI: string
  mRID: string
  reason: string
}

export type CancelResult =
  | { ok: true; cancelled: number }
  | { ok: false; message: string; failed: FailedShare[]; retry: string | null }

const RETRY_PREFIX = `${GRID_REQUESTS_PATH}/`

export const batchCancelPath = (batch: string): string => `${RETRY_PREFIX}${encodeURIComponent(batch)}/cancel`
export const blockCancelPath = (batch: string, row: number): string => `${RETRY_PREFIX}${encodeURIComponent(batch)}/rows/${row}/cancel`

function readFailed(body: unknown): FailedShare[] {
  if (!isObject(body) || !Array.isArray(body.failed)) return []
  return body.failed
    .filter((f): f is Record<string, unknown> => isObject(f) && typeof f.row === 'number')
    .map((f) => ({
      row: f.row as number,
      aggregatorLFDI: typeof f.aggregatorLFDI === 'string' ? f.aggregatorLFDI : '',
      mRID: typeof f.mRID === 'string' ? f.mRID : '',
      reason: typeof f.reason === 'string' ? f.reason : '',
    }))
}

// retryPath is the reply's retry route, taken only when it is a cancel route
// on this API, so a reply cannot send the page's credentialed POST elsewhere.
function retryPath(body: unknown): string | null {
  if (!isObject(body) || typeof body.retry !== 'string') return null
  return body.retry.startsWith(RETRY_PREFIX) && body.retry.endsWith('/cancel') && !body.retry.includes('..') ? body.retry : null
}

// postCancel posts one cancel route. 404 and 409 carry the server's text; a
// 500 lists the shares still live and the route that finishes them.
export async function postCancel(path: string, opts: RequestOptions): Promise<CancelResult> {
  const res = await postJSON<unknown>(path, {}, opts)
  if (res.ok) {
    const cancelled = isObject(res.data) && Array.isArray(res.data.cancelled) ? res.data.cancelled.length : 0
    return { ok: true, cancelled }
  }
  return { ok: false, message: res.error, failed: readFailed(res.body), retry: retryPath(res.body) }
}
