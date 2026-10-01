// GET /api/derms/fleets renders the DERMS tab's fleet list (#671's first
// criterion). The trap this suite is built to catch (also stated in
// admin_fleet.go's FleetSum doc comment): a roll-up over devices that did
// not report must show the count missing, never a filled value, so "every
// device unreported" and "every device stale" both have to render as "no
// devices reporting" rather than a synthesized 0 W - and a REAL 0 W from a
// reporting device must still say "0 W" (PR 730 round 1, finding 2).
//
// PR 730 round 1 also covers: a stale response never overwriting a fresher
// one regardless of arrival order (findings 1, 3), the reading age
// re-deriving on a timer instead of freezing (finding 4), the direction
// word agreeing with the rounded number beside it (finding 5), and an
// unexpected response body reading as an error rather than throwing
// (finding 5).
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/svelte'
import FleetPane from './FleetPane.svelte'
import * as api from '../lib/api'
import type { Fleet } from '../lib/fleet'

function mockFetchJSON(result: { ok: true; data: Fleet[] } | { ok: false; error: string; status: number }) {
  return vi.spyOn(api, 'fetchJSON').mockResolvedValue(result as never)
}

function minimalFleet(aggregatorLFDI: string): Fleet {
  return {
    aggregatorLFDI,
    devices: [],
    rollup: {
      deviceCount: 0,
      connected: 0,
      alarmed: 0,
      stale: 0,
      p: { sum: 0, unreported: 0, stale: 0 },
      q: { sum: 0, unreported: 0, stale: 0 },
      statWAvail: { sum: 0, unreported: 0, stale: 0 },
      statVarAvail: { sum: 0, unreported: 0, stale: 0 },
    },
  }
}

// Field names copied from internal/handler/admin_fleet.go's json tags at
// 643e2d1 (`git show 643e2d1:internal/handler/admin_fleet.go`): Fleet
// {aggregatorLFDI, devices, rollup}, FleetRollup {deviceCount, connected,
// alarmed, stale, p, q, statWAvail, statVarAvail}, FleetSum {sum,
// unreported, stale}. Parsed rather than typed as object literals, so a
// key this pane reads but a renamed Go tag no longer sends shows up as a
// wrong rendered value, not a TypeScript type that silently keeps compiling.
const OLD_FLEET_JSON = `[{
  "aggregatorLFDI": "AGGOLD00000000000000000000000000000001",
  "devices": [],
  "rollup": {
    "deviceCount": 3, "connected": 1, "alarmed": 0, "stale": 0,
    "p": {"sum": 0, "unreported": 3, "stale": 0},
    "q": {"sum": 0, "unreported": 3, "stale": 0},
    "statWAvail": {"sum": 0, "unreported": 3, "stale": 0},
    "statVarAvail": {"sum": 0, "unreported": 3, "stale": 0}
  }
}]`

const NEW_FLEET_JSON = `[{
  "aggregatorLFDI": "AGGNEW00000000000000000000000000000002",
  "devices": [],
  "rollup": {
    "deviceCount": 7, "connected": 5, "alarmed": 1, "stale": 0,
    "p": {"sum": 0, "unreported": 7, "stale": 0},
    "q": {"sum": 0, "unreported": 7, "stale": 0},
    "statWAvail": {"sum": 0, "unreported": 7, "stale": 0},
    "statVarAvail": {"sum": 0, "unreported": 7, "stale": 0}
  }
}]`

beforeEach(() => {
  vi.setSystemTime(new Date(1_700_000_000 * 1000))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
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

  it('renders a measured 0 W from a reporting fleet as "0 W", not "No devices reporting"', async () => {
    // PR 730 round 1, finding 2: kills the mutant at lib/fleet.ts:104
    // (`contributing > 0` replaced by `sum.sum !== 0`), which this exact
    // shape (a real 0 W, fully reported) would pass through unnoticed.
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG-ZERO',
      devices: [{ lfdi: 'DEV1', measurements: { p: { value: 0, readingTime: 1_699_999_990 } } }],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 0, unreported: 0, stale: 0 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 0, unreported: 1, stale: 0 },
        statVarAvail: { sum: 0, unreported: 1, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const power = await screen.findByTestId('fleet-power')
    expect(power).toHaveTextContent('0 W')
    expect(power).not.toHaveTextContent('No devices reporting')
  })

  it('shows no direction word when the sum rounds to 0 W', async () => {
    // PR 730 round 1, finding 5: directionWord used to read the unrounded
    // value, so 0.4 W printed "0 W exporting".
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG-SUBWATT',
      devices: [{ lfdi: 'DEV1', measurements: { p: { value: 0.4, readingTime: 1_699_999_990 } } }],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 0.4, unreported: 0, stale: 0 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 0, unreported: 1, stale: 0 },
        statVarAvail: { sum: 0, unreported: 1, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const power = await screen.findByTestId('fleet-power')
    expect(power).toHaveTextContent('0 W')
    expect(power).not.toHaveTextContent('exporting')
    expect(power).not.toHaveTextContent('importing')
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

  it('shows active capacity independently when reactive has no contributors', async () => {
    // PR 730 round 1, finding 3: kills both the `&&` to `||` mutant and the
    // `{#if true}` mutant at the old combined guard, since active and
    // reactive no longer share one condition.
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG-MIXED-AVAIL-1',
      devices: [{ lfdi: 'DEV1', availability: { statWAvail: 500, readingTime: 1_699_999_995 }, measurements: {} }],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 0, unreported: 1, stale: 0 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 500, unreported: 0, stale: 0 },
        statVarAvail: { sum: 0, unreported: 1, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const active = await screen.findByTestId('fleet-avail-active')
    const reactive = await screen.findByTestId('fleet-avail-reactive')
    expect(active).toHaveTextContent('500 W active')
    expect(reactive).toHaveTextContent('No devices reporting')
    expect(reactive).toHaveTextContent('1 unreported')
  })

  it('shows reactive capacity independently when active has no contributors', async () => {
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG-MIXED-AVAIL-2',
      devices: [{ lfdi: 'DEV1', availability: { statVarAvail: 250, readingTime: 1_699_999_995 }, measurements: {} }],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 0, unreported: 1, stale: 0 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 0, unreported: 1, stale: 0 },
        statVarAvail: { sum: 250, unreported: 0, stale: 0 },
      },
    }
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    const active = await screen.findByTestId('fleet-avail-active')
    const reactive = await screen.findByTestId('fleet-avail-reactive')
    expect(active).toHaveTextContent('No devices reporting')
    expect(active).toHaveTextContent('1 unreported')
    expect(reactive).toHaveTextContent('250 VAR reactive')
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

  it('shows the error state, not a TypeError, when the body is not an array', async () => {
    // PR 730 round 1, finding 5: a 200 whose body is null or otherwise not
    // an array used to throw inside fleets.length before this guard.
    mockFetchJSON({ ok: true, data: null as unknown as Fleet[] })

    render(FleetPane)

    await waitFor(() => {
      expect(screen.getByTestId('fleet-error')).toHaveTextContent('Could not load fleets:')
    })
  })

  it('disables Refresh while a load is in flight', async () => {
    let resolveFirst: ((v: { ok: true; data: Fleet[] }) => void) | undefined
    const pending = new Promise<{ ok: true; data: Fleet[] }>((resolve) => {
      resolveFirst = resolve
    })
    vi.spyOn(api, 'fetchJSON').mockReturnValueOnce(pending as never)

    render(FleetPane)

    const button = await screen.findByRole('button', { name: 'Refresh' })
    expect(button).toBeDisabled()

    resolveFirst?.({ ok: true, data: [] })
    await screen.findByTestId('fleet-empty')
    expect(button).not.toBeDisabled()
  })

  it('replaces old data with new data on Refresh (fixture field names from admin_fleet.go json tags)', async () => {
    const oldFleet = (JSON.parse(OLD_FLEET_JSON) as Fleet[])[0]
    const newFleet = (JSON.parse(NEW_FLEET_JSON) as Fleet[])[0]
    const fetchJSON = vi
      .spyOn(api, 'fetchJSON')
      .mockResolvedValueOnce({ ok: true, data: [oldFleet] } as never)
      .mockResolvedValueOnce({ ok: true, data: [newFleet] } as never)

    render(FleetPane)

    const oldRow = await screen.findByTestId('fleet-row')
    expect(oldRow.children[1]).toHaveTextContent('3')
    expect(oldRow).toHaveTextContent(`${oldFleet.aggregatorLFDI.substring(0, 16)}...`)

    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => {
      expect(screen.getByTestId('fleet-row').children[1]).toHaveTextContent('7')
    })
    expect(screen.getByTestId('fleet-row')).toHaveTextContent(`${newFleet.aggregatorLFDI.substring(0, 16)}...`)
    expect(fetchJSON).toHaveBeenCalledTimes(2)
  })

  it('shows an error when a later Refresh fails, even though the first load succeeded', async () => {
    vi.spyOn(api, 'fetchJSON')
      .mockResolvedValueOnce({ ok: true, data: JSON.parse(OLD_FLEET_JSON) } as never)
      .mockResolvedValueOnce({ ok: false, error: 'refresh failed', status: 500 } as never)

    render(FleetPane)
    await screen.findByTestId('fleet-row')

    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => {
      expect(screen.getByTestId('fleet-error')).toHaveTextContent('refresh failed')
    })
    expect(screen.queryByTestId('fleet-row')).toBeNull()
  })

  it('keeps a newer error result when an older, slower response resolves last carrying data', async () => {
    // PR 730 round 1, finding 1: the sequence guard, "older arrives last"
    // case 1 of 2 (older carries data).
    let resolveOlder: ((v: { ok: true; data: Fleet[] }) => void) | undefined
    const olderPromise = new Promise<{ ok: true; data: Fleet[] }>((resolve) => {
      resolveOlder = resolve
    })
    const fetchJSON = vi
      .spyOn(api, 'fetchJSON')
      .mockImplementationOnce(() => olderPromise as never) // onMount's load: older, held pending
      .mockImplementationOnce(async () => ({ ok: false, error: 'boom', status: 500 }) as never) // Refresh: newer

    render(FleetPane)
    await screen.findByTestId('fleet-loading')

    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(screen.getByTestId('fleet-error')).toHaveTextContent('boom'))

    resolveOlder?.({ ok: true, data: [minimalFleet('AGG-STALE-DATA')] })
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(screen.getByTestId('fleet-error')).toHaveTextContent('boom')
    expect(screen.queryByTestId('fleet-row')).toBeNull()
    expect(fetchJSON).toHaveBeenCalledTimes(2)
  })

  it('keeps newer data when an older, slower response resolves last carrying an error', async () => {
    // PR 730 round 1, finding 1: the sequence guard, "older arrives last"
    // case 2 of 2 (older carries an error).
    let resolveOlder: ((v: { ok: false; error: string; status: number }) => void) | undefined
    const olderPromise = new Promise<{ ok: false; error: string; status: number }>((resolve) => {
      resolveOlder = resolve
    })
    const newer = minimalFleet('AGG-FRESH-DATA')
    vi.spyOn(api, 'fetchJSON')
      .mockImplementationOnce(() => olderPromise as never) // onMount's load: older, held pending
      .mockImplementationOnce(async () => ({ ok: true, data: [newer] }) as never) // Refresh: newer

    render(FleetPane)
    await screen.findByTestId('fleet-loading')

    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    const row = await screen.findByTestId('fleet-row')
    expect(row).toHaveTextContent(newer.aggregatorLFDI.substring(0, 16))

    resolveOlder?.({ ok: false, error: 'stale failure', status: 500 })
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(screen.getByTestId('fleet-row')).toBeInTheDocument()
    expect(screen.queryByTestId('fleet-error')).toBeNull()
  })

  it('does not write state or log an error when a response lands after unmount', async () => {
    let resolveLate: ((v: { ok: false; error: string; status: number }) => void) | undefined
    const pending = new Promise<{ ok: false; error: string; status: number }>((resolve) => {
      resolveLate = resolve
    })
    vi.spyOn(api, 'fetchJSON').mockReturnValueOnce(pending as never)
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    const { unmount } = render(FleetPane)
    unmount()
    resolveLate?.({ ok: false, error: 'too late', status: 500 })
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(consoleError).not.toHaveBeenCalled()
  })

  it('re-derives the reading age on a timer instead of freezing at fetch time', async () => {
    // PR 730 round 1, finding 4. vi.setSystemTime alone (used by every
    // other test above) never fires setInterval; this test needs the
    // timer itself faked to prove the age keeps moving.
    vi.useFakeTimers()
    vi.setSystemTime(1_700_000_000_000)
    const fleet: Fleet = {
      aggregatorLFDI: 'AGG-TICK',
      devices: [{ lfdi: 'DEV1', measurements: { p: { value: 1000, readingTime: 1_699_999_990 } } }],
      rollup: {
        deviceCount: 1,
        connected: 0,
        alarmed: 0,
        stale: 0,
        p: { sum: 1000, unreported: 0, stale: 0 },
        q: { sum: 0, unreported: 1, stale: 0 },
        statWAvail: { sum: 0, unreported: 1, stale: 0 },
        statVarAvail: { sum: 0, unreported: 1, stale: 0 },
      },
    }
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: [fleet] } as never)

    render(FleetPane)
    await vi.advanceTimersByTimeAsync(0) // flush onMount's fetch microtask, no timer tick yet

    expect(screen.getByTestId('fleet-power')).toHaveTextContent('updated 10s ago')

    await vi.advanceTimersByTimeAsync(3600 * 1000) // the clock moves an hour with no refetch

    expect(screen.getByTestId('fleet-power')).toHaveTextContent('updated 1h ago')
  })

  // PR 730 follow-ups (#735). A fetch that honors its AbortSignal but never
  // answers, which is what a hung connection looks like to the pane.
  function hangingFetch() {
    const signals: AbortSignal[] = []
    const fetchMock = vi.fn((_path: string, init?: RequestInit) => {
      const signal = init?.signal as AbortSignal
      signals.push(signal)
      return new Promise<Response>((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    return { signals, fetchMock }
  }

  it('times a hung fetch out into the error state and re-enables Refresh', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(1_700_000_000_000)
    hangingFetch()

    render(FleetPane)
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByTestId('fleet-loading')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeDisabled()

    await vi.advanceTimersByTimeAsync(15_000)

    expect(screen.getByTestId('fleet-error')).toHaveTextContent('Could not load fleets: request timed out')
    expect(screen.getByRole('button', { name: 'Refresh' })).not.toBeDisabled()
  })

  it('shows the error state, not a TypeError, when the array holds a null element', async () => {
    mockFetchJSON({ ok: true, data: [null] as unknown as Fleet[] })

    render(FleetPane)

    await waitFor(() => {
      expect(screen.getByTestId('fleet-error')).toHaveTextContent(
        'Could not load fleets: server returned an unexpected response shape',
      )
    })
    expect(screen.queryByTestId('fleet-row')).toBeNull()
  })

  it('aborts the in-flight request when the component is destroyed', async () => {
    const { signals } = hangingFetch()

    const { unmount } = render(FleetPane)
    await waitFor(() => expect(signals).toHaveLength(1))
    expect(signals[0].aborted).toBe(false)

    unmount()

    expect(signals[0].aborted).toBe(true)
  })

  it('clears the age interval when the component is destroyed', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(1_700_000_000_000)
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: [] } as never)

    const { unmount } = render(FleetPane)
    await vi.advanceTimersByTimeAsync(0)
    expect(vi.getTimerCount()).toBe(1)

    unmount()

    expect(vi.getTimerCount()).toBe(0)
  })

  it('labels each cell when neither availability sum has a contributor', async () => {
    const fleet = minimalFleet('AGG-NO-AVAIL')
    fleet.rollup.deviceCount = 1
    fleet.rollup.statWAvail.unreported = 1
    fleet.rollup.statVarAvail.unreported = 1
    mockFetchJSON({ ok: true, data: [fleet] })

    render(FleetPane)

    expect(await screen.findByTestId('fleet-avail-active')).toHaveTextContent('No devices reporting active')
    expect(screen.getByTestId('fleet-avail-reactive')).toHaveTextContent('No devices reporting reactive')
  })
})
