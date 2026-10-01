// The metered delivery shown beside each control's target: the served
// figure as served, a null as "no readings" and never 0, no direction word
// when the server could not sum every reading, and the target and the
// delivery never compared with each other.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/svelte'
import DispatchPane from './DispatchPane.svelte'
import * as api from '../lib/api'

const LFDI = 'AGG00000000000000000000000000000000001'
const PROGRAM = '/edev/4/derp/1'
const DEVICE_LFDI = 'DEV-LFDI-4-0123456789ABCDEF'
const zero = { sum: 0, unreported: 1, stale: 0 }
const FLEET = {
  aggregatorLFDI: LFDI,
  devices: [{ lfdi: 'DEV-LFDI-4', edevId: '4', href: '/edev/4', measurements: {} }],
  rollup: { deviceCount: 1, connected: 0, alarmed: 0, stale: 0, p: zero, q: zero, statWAvail: zero, statVarAvail: zero },
}

function delivery(over: Record<string, unknown> = {}) {
  const nowS = Math.floor(Date.now() / 1000)
  return {
    windowStart: nowS - 1000,
    windowEnd: nowS - 400,
    deliveredWh: 1234.4,
    averageW: 7400.4,
    coveredSeconds: 450,
    readings: 5,
    directionUnknown: false,
    deviceLFDI: DEVICE_LFDI,
    newestReadingTime: nowS - 125,
    ...over,
  }
}

function controlItem(del: unknown, over: Record<string, unknown> = {}) {
  return {
    mRID: 'CTL00001',
    href: '/edev/4/derp/1/derc/1',
    derProgramHref: PROGRAM,
    derControlListHref: '/edev/4/derp/1/derc',
    type: 'targetW',
    description: '',
    derControlBase: { opModTargetW: { value: -8000, multiplier: 0 } },
    creationTime: 1790000000,
    interval: { start: 1790000000, duration: 1800 },
    eventStatus: { currentStatus: 0, status: 'scheduled', dateTime: 1790000000 },
    executesGrant: null,
    responses: { total: 0, byStatus: {} },
    delivery: del,
    ...over,
  }
}

async function mountWith(del: unknown, ctlOver: Record<string, unknown> = {}) {
  vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    if (path === '/api/derms/fleets') return { ok: true, data: [FLEET] }
    if (path.startsWith('/api/derms/grants')) return { ok: true, data: { aggregatorLFDI: LFDI, now: 1789990000, grants: [] } }
    if (/^\/api\/devices\/[^/]+\/der-programs$/.test(path)) {
      return { ok: true, data: { device: '4', programs: [{ href: PROGRAM, mRID: 'P1', description: 'p', primacy: 0, derControlListHref: PROGRAM + '/derc' }] } }
    }
    if (path.startsWith('/api/der/controls')) return { ok: true, data: { device: '4', controls: [controlItem(del, ctlOver)] } }
    throw new Error('unexpected read ' + path)
  }) as never)
  render(DispatchPane)
  await fireEvent.change(await screen.findByTestId('dispatch-fleet'), { target: { value: LFDI } })
  await fireEvent.click(screen.getByRole('radio', { name: 'Plain dispatch' }))
  await fireEvent.change(await screen.findByTestId('dispatch-device'), { target: { value: '4' } })
  await screen.findByTestId('dispatch-control-row')
  return screen.getByTestId('dispatch-control-delivery')
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('DispatchPane, metered delivery', () => {
  it('shows each served field as served', async () => {
    const cell = await mountWith(delivery())
    expect(screen.getByTestId('dispatch-delivery-wh')).toHaveTextContent('Delivered (export-positive): 1,234 Wh')
    expect(screen.getByTestId('dispatch-delivery-avg')).toHaveTextContent('Average over covered seconds: 7,400 W')
    expect(screen.getByTestId('dispatch-delivery-covered')).toHaveTextContent('Covered 450 s of 600 s')
    expect(screen.getByTestId('dispatch-delivery-newest')).toHaveTextContent(/Newest reading: \d{4}-\d\d-\d\dT[\d:.]+Z \(2m ago\)/)
    expect(screen.getByTestId('dispatch-delivery-source')).toHaveTextContent('Source: mirror readings of device ' + DEVICE_LFDI)
    expect(cell).not.toHaveTextContent('Some readings were not counted')
  })

  it('shows the target in its own frame beside the delivery and the response-lag caveat', async () => {
    await mountWith(delivery())
    expect(screen.getByTestId('dispatch-control-row')).toHaveTextContent('-8,000 W (DER frame)')
    expect(screen.getByTestId('dispatch-delivery-note')).toHaveTextContent('may take minutes to take effect')
  })

  it('reads a null figure as "no readings", never 0', async () => {
    await mountWith(delivery({ deliveredWh: null, averageW: null, coveredSeconds: 0, readings: 0, newestReadingTime: null }))
    expect(screen.getByTestId('dispatch-delivery-wh')).toHaveTextContent('Delivered (export-positive): no readings')
    expect(screen.getByTestId('dispatch-delivery-avg')).toHaveTextContent('Average over covered seconds: no readings')
    expect(screen.getByTestId('dispatch-delivery-newest')).toHaveTextContent('Newest reading: no readings')
    expect(screen.getByTestId('dispatch-delivery-wh')).not.toHaveTextContent(/\b0 Wh/)
    expect(screen.getByTestId('dispatch-delivery-avg')).not.toHaveTextContent(/\b0 W/)
  })

  it('shows a small non-zero delivery as non-zero and a true 0 as 0', async () => {
    await mountWith(delivery({ deliveredWh: 0.4, averageW: 0.02 }))
    expect(screen.getByTestId('dispatch-delivery-wh')).toHaveTextContent('Delivered (export-positive): 0.4 Wh')
    expect(screen.getByTestId('dispatch-delivery-avg')).toHaveTextContent('Average over covered seconds: <0.1 W')
    cleanup()
    await mountWith(delivery({ deliveredWh: 0, averageW: 0 }))
    expect(screen.getByTestId('dispatch-delivery-wh')).toHaveTextContent('Delivered (export-positive): 0 Wh')
    expect(screen.getByTestId('dispatch-delivery-avg')).toHaveTextContent('Average over covered seconds: 0 W')
  })

  it('says "so far" while the control is active and its window ends before its interval', async () => {
    await mountWith(delivery({ windowStart: 1790000000, windowEnd: 1790000600 }), {
      eventStatus: { currentStatus: 1, status: 'active', dateTime: 1790000000 },
    })
    expect(screen.getByTestId('dispatch-delivery-covered')).toHaveTextContent('Covered 450 s of 600 s so far')
  })

  it('does not say "so far" for a closed window: ended interval, cancelled or scheduled', async () => {
    await mountWith(delivery({ windowStart: 1790000000, windowEnd: 1790001800 }), {
      eventStatus: { currentStatus: 1, status: 'active', dateTime: 1790000000 },
    })
    expect(screen.getByTestId('dispatch-delivery-covered')).not.toHaveTextContent('so far')
    cleanup()
    await mountWith(delivery({ windowStart: 1790000000, windowEnd: 1790000600 }), {
      eventStatus: { currentStatus: 2, status: 'cancelled', dateTime: 1790000600 },
    })
    expect(screen.getByTestId('dispatch-delivery-covered')).toHaveTextContent('Covered 450 s of 600 s')
    expect(screen.getByTestId('dispatch-delivery-covered')).not.toHaveTextContent('so far')
  })

  it('reads a missing delivery object as "no readings"', async () => {
    const cell = await mountWith(null)
    expect(cell).toHaveTextContent('no readings')
  })

  it('says some readings were not counted and shows no direction word when the direction is unknown', async () => {
    const cell = await mountWith(delivery({ directionUnknown: true }))
    expect(screen.getByTestId('dispatch-delivery-direction')).toHaveTextContent('Some readings were not counted')
    expect(cell.textContent ?? '').not.toMatch(/export|import|charge|discharge/i)
  })

  it('never subtracts or compares the delivery with the target', async () => {
    const cell = await mountWith(delivery({ deliveredWh: 1234.4, averageW: 7400.4 }))
    const text = cell.textContent ?? ''
    expect(text).not.toContain('%')
    // 8000 - 7400 = 600 and 8000 + 7400 = 15,400 would be the tell.
    expect(text).not.toMatch(/\b600 W|15,400|-600|-15,400/)
    expect(text).not.toMatch(/of target|vs target|shortfall|difference/i)
  })
})
