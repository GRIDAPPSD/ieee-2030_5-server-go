// The grid block list's narrowing and liveness. What it must get right: a
// null delivered figure stays null, a block is live while any share can still
// be cancelled (by canCancel, on the server's clock), blocks sort by start,
// and a malformed reply is refused whole.
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from './api'
import {
  batchCancelPath,
  blockCancelPath,
  blockEvent,
  isLiveBlock,
  parseGridBlocks,
  postCancel,
  shownBlocks,
  type BlockEntry,
} from './gridblocks'

const share = (status: string, over: Record<string, unknown> = {}) => ({
  aggregatorLFDI: 'AGG1',
  devices: 2,
  status,
  aggregatorGone: false,
  deliveredW: null,
  mRID: `M-${status}`,
  ...over,
})

const block = (row: number, start: number, status: string, shares = [share(status)], over: Record<string, unknown> = {}) => ({
  row,
  direction: row % 2 === 0 ? 'charge' : 'discharge',
  requestedW: row % 2 === 0 ? -20000 : 50000,
  encodedW: row % 2 === 0 ? -20000 : 50000,
  differenceW: 0,
  start,
  end: start + 600,
  durationSeconds: 600,
  status,
  basisChanged: false,
  currentSplit: [],
  deliveredW: null,
  shares,
  ...over,
})

const wire = (rows: unknown[]) => ({ batches: [{ batch: 'b1', rows }] })

afterEach(() => vi.restoreAllMocks())

describe('parseGridBlocks', () => {
  it('sorts by start and keeps a null delivered figure null, with the fields as sent', () => {
    const out = parseGridBlocks(wire([block(2, 2000, 'scheduled'), block(1, 1000, 'active', [share('active', { deliveredW: 0 })], { deliveredW: 0 })]))
    expect(out?.map((e) => [e.batch, e.block.row, e.block.start])).toEqual([['b1', 1, 1000], ['b1', 2, 2000]])
    expect(out?.[0].block.deliveredW).toBe(0)
    expect(out?.[0].block.shares[0].deliveredW).toBe(0)
    expect(out?.[1].block.deliveredW).toBeNull()
    expect(out?.[1].block.shares[0].deliveredW).toBeNull()
    expect(out?.[1].block.requestedW).toBe(-20000)
  })

  it('reads an empty list as no blocks and refuses a reply with a bad field whole', () => {
    expect(parseGridBlocks({ batches: [] })).toEqual([])
    expect(parseGridBlocks(wire([block(1, 1, 'scheduled', [share('scheduled', { deliveredW: '5' })])]))).toBeNull()
    expect(parseGridBlocks(wire([block(1, 1, 'scheduled', undefined, { direction: 'sideways' })]))).toBeNull()
    expect(parseGridBlocks({ nope: 1 })).toBeNull()
  })
})

const entry = (b: ReturnType<typeof block>): BlockEntry => (parseGridBlocks(wire([b])) as BlockEntry[])[0]

describe('isLiveBlock', () => {
  it('is live while any share can be cancelled, even when the block reads partly cancelled or mixed', () => {
    const mixed = entry(block(1, 1000, 'partly cancelled', [share('cancelled'), share('scheduled')]))
    expect(isLiveBlock(mixed, 0)).toBe(true)
    const done = entry(block(1, 1000, 'cancelled', [share('cancelled'), share('cancelled')]))
    expect(isLiveBlock(done, 0)).toBe(false)
    expect(isLiveBlock(entry(block(1, 1000, 'completed')), 0)).toBe(false)
  })

  it('an active block ends on the server clock, and stays live with no server time', () => {
    const e = entry(block(1, 1000, 'active')) // ends at 1600
    expect(isLiveBlock(e, 1599)).toBe(true)
    expect(isLiveBlock(e, 1600)).toBe(false)
    expect(isLiveBlock(e, null)).toBe(true)
  })

  it('shownBlocks drops the not-live ones only when the filter is on', () => {
    const all = [entry(block(1, 1000, 'scheduled')), entry(block(2, 2000, 'cancelled'))]
    expect(shownBlocks(all, true, 0).map((e) => e.block.row)).toEqual([1])
    expect(shownBlocks(all, false, 0).map((e) => e.block.row)).toEqual([1, 2])
  })
})

describe('blockEvent', () => {
  it('carries direction, encoded target and a status the timeline knows, else unknown', () => {
    const ev = blockEvent(entry(block(2, 1000, 'mixed')))
    expect(ev.direction).toBe('charge')
    expect(ev.targetW).toBe(-20000)
    expect(ev.status).toBe('unknown')
    expect(ev.mrid).toBe('b1:2')
    expect(blockEvent(entry(block(1, 1000, 'active'))).status).toBe('active')
  })
})

describe('postCancel', () => {
  it('names the batch and block routes', () => {
    expect(batchCancelPath('b1')).toBe('/api/derms/grid-requests/b1/cancel')
    expect(blockCancelPath('b1', 3)).toBe('/api/derms/grid-requests/b1/rows/3/cancel')
  })

  it('counts what a 200 cancelled', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { batch: 'b1', cancelled: [{ row: 1 }, { row: 1 }], failed: [] } })
    expect(await postCancel('/x', {})).toEqual({ ok: true, cancelled: 2 })
  })

  it('reads a 500 into its failed shares and a retry route, and refuses a retry route off this API', async () => {
    const failed = [{ row: 2, aggregatorLFDI: 'AGG1', mRID: 'M1', reason: 'copy write failed' }]
    const body = { batch: 'b1', failed, error: 'x', retry: '/api/derms/grid-requests/b1/cancel' }
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, status: 500, error: 'x', body })
    expect(await postCancel('/x', {})).toEqual({ ok: false, message: 'x', failed, retry: '/api/derms/grid-requests/b1/cancel' })
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, status: 500, error: 'x', body: { ...body, retry: 'https://evil.example/cancel' } })
    expect((await postCancel('/x', {})) as { retry: string | null }).toMatchObject({ retry: null })
  })

  it('carries the text of a 409 and a 404 with no failed shares', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, status: 409, error: 'nothing left to cancel' })
    expect(await postCancel('/x', {})).toEqual({ ok: false, message: 'nothing left to cancel', failed: [], retry: null })
  })
})
