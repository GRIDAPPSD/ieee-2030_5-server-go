// The grid blocks pane against a mocked list and cancel. What it must get
// right: every shown block appears once in the timeline and once in the list
// with its direction word, the filter is canCancel's, a cancel sends nothing
// before Confirm and both views change only from the next read, a failed
// share and the retry route are shown, and a null delivered figure never
// reads as zero.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import GridBlocksPane from './GridBlocksPane.svelte'
import * as api from '../lib/api'

const NOW = Math.floor(Date.now() / 1000)
const AGG = 'AGG00000000000000000000000000000000001'

const share = (status: string, over: Record<string, unknown> = {}) => ({
  aggregatorLFDI: AGG,
  devices: 2,
  status,
  aggregatorGone: false,
  deliveredW: null,
  mRID: `M-${status}`,
  ...over,
})

function block(row: number, offsetMin: number, status: string, over: Record<string, unknown> = {}) {
  const discharge = row % 2 === 1
  return {
    row,
    direction: discharge ? 'discharge' : 'charge',
    requestedW: discharge ? 50000 : -20000,
    encodedW: discharge ? 50000 : -20000,
    differenceW: 0,
    start: NOW + offsetMin * 60,
    end: NOW + (offsetMin + 30) * 60,
    durationSeconds: 1800,
    status,
    basisChanged: false,
    currentSplit: [],
    deliveredW: null,
    shares: [share(status)],
    ...over,
  }
}

let server: { batches: { batch: string; rows: ReturnType<typeof block>[] }[] }

function listOf(rows: ReturnType<typeof block>[]) {
  server = { batches: [{ batch: 'b1', rows }] }
}

function mockGet() {
  return vi.spyOn(api, 'fetchJSON').mockImplementation((async () => ({ ok: true, data: structuredClone(server), serverTime: NOW * 1000 })) as never)
}

const timeline = () => screen.getByTestId('grid-timeline')
const bars = () => within(timeline()).queryAllByTestId('events-bar')
const listRows = () => screen.queryAllByTestId('grid-block')

async function mount() {
  const view = render(GridBlocksPane)
  await screen.findByTestId('grid-blocks-table')
  return view
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('GridBlocksPane views', () => {
  it('shows each block once in the timeline and once in the list, with its direction word, discharge above and charge below', async () => {
    listOf([block(1, 10, 'scheduled'), block(2, 60, 'scheduled')])
    mockGet()
    await mount()
    expect(listRows().map((r) => r.dataset.block)).toEqual(['b1:1', 'b1:2'])
    expect(listRows().map((r) => within(r).getByTestId('grid-block-direction').textContent)).toEqual(['DISCHARGE', 'CHARGE'])
    expect(bars().map((b) => b.dataset.block)).toEqual(['b1:1', 'b1:2'])
    const up = within(screen.getByTestId('grid-timeline-discharge')).getAllByTestId('events-bar')
    const down = within(screen.getByTestId('grid-timeline-charge')).getAllByTestId('events-bar')
    expect(up.map((b) => b.dataset.block)).toEqual(['b1:1'])
    expect(down.map((b) => b.dataset.block)).toEqual(['b1:2'])
    expect(up[0]).toHaveTextContent('discharge 50.0 kW')
    expect(down[0]).toHaveTextContent('charge 20.0 kW')
  })

  it('stacks two short blocks of one direction in separate rows so their labels do not overlap', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
    listOf([block(1, 10, 'scheduled', { end: NOW + 20 * 60 }), block(3, 40, 'scheduled', { start: NOW + 40 * 60, end: NOW + 50 * 60 })])
    mockGet()
    await mount()
    const tops = bars().map((b) => b.style.top)
    expect(tops).toEqual(['calc(0 * var(--ev-bar-h))', 'calc(1 * var(--ev-bar-h))'])
  })

  it('lists blocks by start, whatever order the server sent them in', async () => {
    listOf([block(2, 90, 'scheduled'), block(1, 10, 'scheduled')])
    mockGet()
    await mount()
    expect(listRows().map((r) => r.dataset.block)).toEqual(['b1:1', 'b1:2'])
  })

  it('applies the pending and active filter by canCancel to both views, and shows everything when it is off', async () => {
    listOf([block(1, 10, 'scheduled'), block(2, 60, 'cancelled', { shares: [share('cancelled')] }), block(3, -120, 'active', { start: NOW - 5400, end: NOW - 2700 })])
    mockGet()
    await mount()
    // Block 3 reads active but ended an hour ago on the server clock, so it is not live.
    expect(listRows().map((r) => r.dataset.block)).toEqual(['b1:1'])
    expect(bars().map((b) => b.dataset.block)).toEqual(['b1:1'])
    expect(screen.getByTestId('grid-blocks-live-count')).toHaveTextContent('1 pending or active')
    await fireEvent.click(screen.getByTestId('grid-blocks-live-only'))
    expect(listRows().map((r) => r.dataset.block)).toEqual(['b1:3', 'b1:1', 'b1:2'])
    expect(bars().map((b) => b.dataset.block).sort()).toEqual(['b1:1', 'b1:2', 'b1:3'])
    expect(within(listRows()[2]).queryByRole('button', { name: /^Cancel block/ })).toBeNull()
  })

  it('keeps a partly cancelled block listed while one share is live', async () => {
    listOf([block(1, 10, 'partly cancelled', { shares: [share('cancelled'), share('scheduled')] })])
    mockGet()
    await mount()
    expect(listRows()).toHaveLength(1)
    expect(within(listRows()[0]).getByTestId('grid-block-status')).toHaveTextContent('partly cancelled')
  })

  it('shows a null delivered figure as no reading and a zero as 0 W, with the aggregator and basis flags', async () => {
    listOf([
      block(1, 10, 'scheduled', { basisChanged: true, shares: [share('scheduled', { aggregatorGone: true })] }),
      block(3, 60, 'scheduled', { deliveredW: 0, shares: [share('scheduled', { deliveredW: 0 })] }),
    ])
    mockGet()
    await mount()
    const [a, b] = listRows()
    expect(within(a).getByTestId('grid-block-delivered')).toHaveTextContent('no reading')
    expect(within(a).getByTestId('grid-block-basis')).toBeInTheDocument()
    expect(within(a).getByTestId('grid-block-gone')).toHaveTextContent('gone')
    expect(within(b).getByTestId('grid-block-delivered')).toHaveTextContent('0 W')
    expect(within(b).queryByTestId('grid-block-basis')).toBeNull()
    expect(within(b).queryByTestId('grid-block-gone')).toBeNull()
  })

  it('says so when the server sends a reply it cannot read, and draws nothing from it', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: { batches: [{ batch: 'b', rows: [{ row: 1 }] }] } } as never)
    render(GridBlocksPane)
    expect(await screen.findByTestId('grid-blocks-error')).toHaveTextContent('could not be read')
    expect(listRows()).toHaveLength(0)
  })
})

describe('GridBlocksPane cancel', () => {
  it('sends nothing before Confirm, then updates both views from the next read, not from the answer', async () => {
    listOf([block(1, 10, 'scheduled'), block(2, 60, 'scheduled')])
    const get = mockGet()
    const post = vi.spyOn(api, 'postJSON').mockImplementation((async () => {
      // The server cancels block 2, but the page may show it only after a read.
      server.batches[0].rows[1].status = 'cancelled'
      server.batches[0].rows[1].shares = [share('cancelled')]
      return { ok: true, data: { batch: 'b1', row: 2, cancelled: [{ row: 2 }], failed: [] } }
    }) as never)
    await mount()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel block 2 of batch b1' }))
    expect(post).not.toHaveBeenCalled()
    const readsBefore = get.mock.calls.length
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm cancel block 2 of batch b1' }))
    expect(post).toHaveBeenCalledTimes(1)
    expect(post.mock.calls[0][0]).toBe('/api/derms/grid-requests/b1/rows/2/cancel')
    await waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(readsBefore))
    await waitFor(() => expect(listRows().map((r) => r.dataset.block)).toEqual(['b1:1']))
    expect(bars().map((b) => b.dataset.block)).toEqual(['b1:1'])
  })

  it('cancels a whole batch through the batch route', async () => {
    listOf([block(1, 10, 'scheduled'), block(2, 60, 'scheduled')])
    mockGet()
    const post = vi.spyOn(api, 'postJSON').mockImplementation((async () => {
      for (const r of server.batches[0].rows) {
        r.status = 'cancelled'
        r.shares = [share('cancelled')]
      }
      return { ok: true, data: { batch: 'b1', cancelled: [{ row: 1 }, { row: 2 }], failed: [] } }
    }) as never)
    await mount()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel batch b1' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm cancel batch b1' }))
    expect(post.mock.calls[0][0]).toBe('/api/derms/grid-requests/b1/cancel')
    await waitFor(() => expect(listRows()).toHaveLength(0))
    expect(bars()).toHaveLength(0)
    expect(screen.getByTestId('grid-blocks-empty')).toHaveTextContent('No pending or active blocks')
  })

  it('shows each failed share on a 500 and retries through the reply\'s route', async () => {
    listOf([block(1, 10, 'scheduled')])
    mockGet()
    const failed = [{ row: 1, aggregatorLFDI: AGG, mRID: 'M-scheduled', reason: 'a copy could not be written' }]
    const post = vi
      .spyOn(api, 'postJSON')
      .mockResolvedValueOnce({ ok: false, status: 500, error: '1 of 1 shares could not be cancelled', body: { batch: 'b1', failed, retry: '/api/derms/grid-requests/b1/cancel' } } as never)
      .mockResolvedValueOnce({ ok: true, data: { batch: 'b1', cancelled: [{ row: 1 }], failed: [] } } as never)
    await mount()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel block 1 of batch b1' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm cancel block 1 of batch b1' }))
    const box = await screen.findByTestId('grid-cancel-error')
    expect(box).toHaveTextContent('1 of 1 shares could not be cancelled')
    expect(within(box).getByTestId('grid-cancel-failed')).toHaveTextContent('Block 1')
    expect(within(box).getByTestId('grid-cancel-failed')).toHaveTextContent('(M-scheduled): a copy could not be written')
    await fireEvent.click(within(box).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(post.mock.calls[1][0]).toBe('/api/derms/grid-requests/b1/cancel')
    await waitFor(() => expect(screen.queryByTestId('grid-cancel-error')).toBeNull())
  })

  it('shows the text of a 409 and a 404', async () => {
    listOf([block(1, 10, 'scheduled')])
    mockGet()
    const post = vi
      .spyOn(api, 'postJSON')
      .mockResolvedValueOnce({ ok: false, status: 409, error: 'nothing left to cancel' } as never)
      .mockResolvedValueOnce({ ok: false, status: 404, error: 'no such grid request or block' } as never)
    await mount()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel block 1 of batch b1' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm cancel block 1 of batch b1' }))
    expect(await screen.findByTestId('grid-cancel-error')).toHaveTextContent('nothing left to cancel')
    expect(screen.queryByTestId('grid-cancel-retry')).toBeNull()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel block 1 of batch b1' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm cancel block 1 of batch b1' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(await screen.findByTestId('grid-cancel-error')).toHaveTextContent('no such grid request or block')
  })
})
