// GET /api/derms/fleets renders the DERMS tab's fleet list (#671's first
// criterion). The trap this suite is built to catch (also stated in
// admin_fleet.go's FleetSum doc comment): a roll-up over devices that did
// not report must show the count missing, never a filled value, so "every
// device unreported" and "every device stale" both have to render as "no
// devices reporting" rather than a synthesized 0 W.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/svelte'
import FleetPane from './FleetPane.svelte'
import * as api from '../lib/api'
import type { Fleet } from '../lib/fleet'

function mockFetchJSON(result: { ok: true; data: Fleet[] } | { ok: false; error: string; status: number }) {
  return vi.spyOn(api, 'fetchJSON').mockResolvedValue(result as never)
}

beforeEach(() => {
  vi.setSystemTime(new Date(1_700_000_000 * 1000))
})

afterEach(() => {
  vi.useRealTimers()
})

describe('FleetPane', () => {
  it('renders fleet size, status counts, and a sign-worded, aged measured-power figure', async () => {
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG00000000000000000000000000000000001',
      devices: [
        {
          lfdi: 'DEV1',
          status: { connected: true, readingTime: 1_699_999_900 },
          measurements: { p: { value: 1500, readingTime: 1_699_999_940 } },
        },
        {
          lfdi: 'DEV2',
          status: { connected: true, readingTime: 1_699_999_800 },
          measurements: { p: { value: 500, readingTime: 1_699_999_700 } },
        },
      ],
      rollup: {
        deviceCount: 2,
        connected: 2,
        alarmed: 0,
        stale: 0,
        p: { sum: 2000, unreported: 0, stale: 0 },
        q: { sum: 0, unreported: 2, stale: 0 },
        statWAvail: { sum: 0, unreported: 2, stale: 0 },
        statVarAvail: { sum: 0, unreported: 2, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const row = await screen.findByTestId('fleet-row')
    expect(row).toHaveTextContent(`${fleet.aggregatorLFDI.substring(0, 16)}...`)
    expect(row.children[1]).toHaveTextContent('2') // devices
    expect(row.children[2]).toHaveTextContent('2') // connected
    expect(row.children[3]).toHaveTextContent('0') // alarmed
    expect(row.children[4]).toHaveTextContent('0') // stale
    expect(row.children[5]).toHaveTextContent('0') // unreported status

    const power = screen.getByTestId('fleet-power')
    expect(power).toHaveTextContent('2,000 W exporting')
    // Newest P reading is 60s before the mocked "now".
    expect(power).toHaveTextContent('updated 1m ago')
  })

  it('names importing for a negative export-positive sum', async () => {
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG2',
      devices: [{ lfdi: 'DEV1', measurements: { p: { value: -300, readingTime: 1_699_999_970 } } }],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: -300, unreported: 0, stale: 0 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 0, unreported: 1, stale: 0 },
        statVarAvail: { sum: 0, unreported: 1, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const power = await screen.findByTestId('fleet-power')
    expect(power).toHaveTextContent('300 W importing')
  })

  it('shows "no devices reporting" rather than 0 W when every device is unreported (the trap)', async () => {
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG3',
      devices: [
        { lfdi: 'DEV1', measurements: {} },
        { lfdi: 'DEV2', measurements: {} },
      ],
      rollup: {
        deviceCount: 2,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 0, unreported: 2, stale: 0 },
        q: { sum: 0, unreported: 2, stale: 0 },
        statWAvail: { sum: 0, unreported: 2, stale: 0 },
        statVarAvail: { sum: 0, unreported: 2, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const power = await screen.findByTestId('fleet-power')
    expect(power).toHaveTextContent('No devices reporting')
    expect(power).not.toHaveTextContent('0 W')
  })

  it('shows "no devices reporting" when every device is stale, even though sum.sum is also 0', async () => {
    // A stale device's value is excluded from Sum server-side (addToSum in
    // admin_fleet.go), so sum.sum reads 0 here exactly as it would for an
    // unreported fleet; the two must still be told apart from a real 0 W
    // and are, since both hit sumFigure's 'none' branch.
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG4',
      devices: [{ lfdi: 'DEV1', measurements: { p: { value: 0, readingTime: 1_699_000_000 } } }],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 1,
        p: { sum: 0, unreported: 0, stale: 1 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 0, unreported: 1, stale: 0 },
        statVarAvail: { sum: 0, unreported: 1, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const power = await screen.findByTestId('fleet-power')
    expect(power).toHaveTextContent('No devices reporting')
  })

  it('shows the unreported and stale counts beside a partial sum', async () => {
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG5',
      devices: [
        { lfdi: 'DEV1', measurements: { p: { value: 900, readingTime: 1_699_999_990 } } },
        { lfdi: 'DEV2', measurements: {} },
        { lfdi: 'DEV3', measurements: { p: { value: 0, readingTime: 1_000 } } },
      ],
      rollup: {
        deviceCount: 3,
        connected: 0,
        alarmed: 0,
        stale: 1,
        p: { sum: 900, unreported: 1, stale: 1 },
        q: { sum: 0, unreported: 3, stale: 0 },
        statWAvail: { sum: 0, unreported: 3, stale: 0 },
        statVarAvail: { sum: 0, unreported: 3, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const power = await screen.findByTestId('fleet-power')
    expect(power).toHaveTextContent('900 W exporting')
    expect(power).toHaveTextContent('1 unreported, 1 stale')
  })

  it('renders active and reactive available capacity with their shared reading age', async () => {
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG6',
      devices: [
        {
          lfdi: 'DEV1',
          availability: { statWAvail: 4000, statVarAvail: 1200, readingTime: 1_699_999_400 },
          measurements: {},
        },
      ],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 0, unreported: 1, stale: 0 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 4000, unreported: 0, stale: 0 },
        statVarAvail: { sum: 1200, unreported: 0, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const avail = await screen.findByTestId('fleet-avail')
    expect(avail).toHaveTextContent('4,000 W active')
    expect(avail).toHaveTextContent('1,200 VAR reactive')
    expect(avail).toHaveTextContent('updated 10m ago')
  })

  it('shows the unreported status count for a device with no status field at all', async () => {
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG7',
      devices: [
        { lfdi: 'DEV1', status: { readingTime: 1_699_999_990 }, measurements: {} },
        { lfdi: 'DEV2', measurements: {} },
      ],
      rollup: {
        deviceCount: 2,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 0, unreported: 2, stale: 0 },
        q: { sum: 0, unreported: 2, stale: 0 },
        statWAvail: { sum: 0, unreported: 2, stale: 0 },
        statVarAvail: { sum: 0, unreported: 2, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const row = await screen.findByTestId('fleet-row')
    expect(row.children[5]).toHaveTextContent('1')
  })

  it('shows an empty-fleet message rather than an empty table', async () => {
    mockFetchJSON({ ok: true, data: [] })

    render(FleetPane)

    await screen.findByTestId('fleet-empty')
    expect(screen.queryByTestId('fleet-row')).toBeNull()
  })

  it('reports a fetch failure rather than leaving the card blank', async () => {
    mockFetchJSON({ ok: false, error: 'admin session required', status: 401 })

    render(FleetPane)

    await waitFor(() => {
      expect(screen.getByTestId('fleet-error')).toHaveTextContent('Could not load fleets: admin session required')
    })
  })

  it('reloads the fleet list on Refresh', async () => {
    const fetchJSON = mockFetchJSON({ ok: true, data: [] })

    render(FleetPane)
    await screen.findByTestId('fleet-empty')

    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => expect(fetchJSON).toHaveBeenCalledTimes(2))
    expect(fetchJSON).toHaveBeenCalledWith('/api/derms/fleets')
  })
})
