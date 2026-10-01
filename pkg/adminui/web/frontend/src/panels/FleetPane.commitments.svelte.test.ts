// #803: each fleet's current commitments, read from
// GET /api/derms/commitments. The traps: a failed read must never render as
// "none" (an empty list says the fleet is free), and every sign and
// direction is shown as the route sent it.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import FleetPane from './FleetPane.svelte'
import * as api from '../lib/api'
import type { Fleet } from '../lib/fleet'
import type { Commitments } from '../lib/commitments'

function fleet(lfdi: string): Fleet {
  const zero = { sum: 0, unreported: 0, stale: 0 }
  return {
    aggregatorLFDI: lfdi,
    devices: [],
    rollup: { deviceCount: 0, connected: 0, alarmed: 0, stale: 0, p: zero, q: zero, statWAvail: zero, statVarAvail: zero },
  }
}

type Reply = { ok: true; data: unknown } | { ok: false; error: string; status: number }

function mockRoutes(fleets: Fleet[], byLFDI: Record<string, Reply>) {
  return vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    if (path === '/api/derms/fleets') return { ok: true, data: fleets }
    const lfdi = decodeURIComponent(path.split('aggregatorLFDI=')[1])
    return byLFDI[lfdi]
  }) as never)
}

function commitments(lfdi: string, over: Partial<Commitments> = {}): Commitments {
  return { aggregatorLFDI: lfdi, now: 1_700_000_000, grants: [], plainControls: [], ...over }
}

const GRANT = {
  mRID: 'G1',
  edevId: 'E1',
  frqId: 'F1',
  window: { start: 1_700_000_000, duration: 1800 },
  direction: 'discharge',
  powerW: 5000,
  energyWh: 2500,
  energyRemainingWh: 1200,
}

beforeEach(() => {
  vi.setSystemTime(new Date(1_700_000_000 * 1000))
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('FleetPane commitments column', () => {
  it('renders each grant field as the route sent it', async () => {
    mockRoutes([fleet('AGGA')], {
      AGGA: { ok: true, data: commitments('AGGA', { grants: [GRANT] }) },
    })
    render(FleetPane)
    const grant = await screen.findByTestId('fleet-grant')
    expect(grant).toHaveTextContent('2023-11-14 22:13:20 UTC for 30 min')
    expect(grant).toHaveTextContent('discharge')
    expect(grant).toHaveTextContent('5,000 W')
    expect(grant).toHaveTextContent('1,200 Wh left')
  })

  it('renders each plain control with its interval and signed target', async () => {
    mockRoutes([fleet('AGGB')], {
      AGGB: {
        ok: true,
        data: commitments('AGGB', {
          plainControls: [{ mRID: 'C1', edevId: 'E1', window: { start: 1_700_000_000, duration: 600 }, targetW: -300 }],
        }),
      },
    })
    render(FleetPane)
    const ctl = await screen.findByTestId('fleet-control')
    expect(ctl).toHaveTextContent('2023-11-14 22:13:20 UTC for 10 min')
    expect(ctl).toHaveTextContent('target -300 W (discharge positive)')
  })

  it('says none for a fleet with no commitments, and names the source', async () => {
    mockRoutes([fleet('AGGC')], { AGGC: { ok: true, data: commitments('AGGC') } })
    render(FleetPane)
    expect(await screen.findByTestId('fleet-commitments-none')).toHaveTextContent('none')
    expect(screen.getByTestId('fleet-commitments-source')).toHaveTextContent("this server's records")
  })

  it('says a 500 failed and never none', async () => {
    mockRoutes([fleet('AGGD')], { AGGD: { ok: false, error: 'commitments read failed, see server log', status: 500 } })
    render(FleetPane)
    expect(await screen.findByTestId('fleet-commitments-error')).toHaveTextContent(
      'Could not read commitments: commitments read failed, see server log',
    )
    expect(screen.queryByTestId('fleet-commitments-none')).toBeNull()
  })

  it('says a 404 means the fleet was not found and never none', async () => {
    mockRoutes([fleet('AGGE')], { AGGE: { ok: false, error: 'no such fleet', status: 404 } })
    render(FleetPane)
    expect(await screen.findByTestId('fleet-commitments-error')).toHaveTextContent('fleet not found on the server')
    expect(screen.queryByTestId('fleet-commitments-none')).toBeNull()
  })

  it('treats a malformed body as a failed read, not none', async () => {
    mockRoutes([fleet('AGGF')], { AGGF: { ok: true, data: { aggregatorLFDI: 'AGGF' } } })
    render(FleetPane)
    expect(await screen.findByTestId('fleet-commitments-error')).toHaveTextContent('unexpected response shape')
    expect(screen.queryByTestId('fleet-commitments-none')).toBeNull()
  })

  it('keeps one fleet failing from changing another fleet', async () => {
    mockRoutes([fleet('AGGG'), fleet('AGGH')], {
      AGGG: { ok: false, error: 'boom', status: 500 },
      AGGH: { ok: true, data: commitments('AGGH') },
    })
    render(FleetPane)
    await screen.findAllByTestId('fleet-commitments-source')
    const rows = await screen.findAllByTestId('fleet-row')
    await within(rows[1]).findByTestId('fleet-commitments-none')
    expect(within(rows[0]).getByTestId('fleet-commitments-error')).toBeInTheDocument()
    expect(within(rows[0]).queryByTestId('fleet-commitments-none')).toBeNull()
  })

  it('shows the direction exactly as sent and never derives one from the sign', async () => {
    mockRoutes([fleet('AGGI')], {
      AGGI: {
        ok: true,
        data: commitments('AGGI', {
          grants: [
            { ...GRANT, mRID: 'G2', direction: 'charge', powerW: 4000, energyRemainingWh: -50 },
            { ...GRANT, mRID: 'G3', direction: null, powerW: 4000 },
          ],
        }),
      },
    })
    render(FleetPane)
    const grants = await screen.findAllByTestId('fleet-grant')
    expect(grants[0]).toHaveTextContent('charge, 4,000 W, -50 Wh left')
    expect(grants[0]).not.toHaveTextContent('discharge')
    expect(grants[1]).toHaveTextContent('direction not sent')
    expect(grants[1]).not.toHaveTextContent(/\bcharge\b/)
  })

  it('shows no reading age on a grant', async () => {
    mockRoutes([fleet('AGGJ')], { AGGJ: { ok: true, data: commitments('AGGJ', { grants: [GRANT] }) } })
    render(FleetPane)
    const cell = (await screen.findByTestId('fleet-grant')).closest('td') as HTMLElement
    expect(cell).not.toHaveTextContent(/updated|ago/)
  })

  it('states a missing quantity in words rather than as zero', async () => {
    mockRoutes([fleet('AGGK')], {
      AGGK: {
        ok: true,
        data: commitments('AGGK', { grants: [{ ...GRANT, powerW: null, energyRemainingWh: null }] }),
      },
    })
    render(FleetPane)
    const grant = await screen.findByTestId('fleet-grant')
    expect(grant).toHaveTextContent('power not sent')
    expect(grant).toHaveTextContent('energy left not sent')
    expect(grant).not.toHaveTextContent('0 W')
  })

  it('drops a superseded commitments reply that lands after a Refresh', async () => {
    const calls: string[] = []
    let releaseFirst: (v: unknown) => void = () => {}
    const first = new Promise((resolve) => {
      releaseFirst = resolve
    })
    vi.spyOn(api, 'fetchJSON').mockImplementation(((path: string) => {
      if (path === '/api/derms/fleets') return Promise.resolve({ ok: true, data: [fleet('AGGR')] })
      calls.push(path)
      if (calls.length === 1) return first
      return Promise.resolve({ ok: true, data: commitments('AGGR', { grants: [{ ...GRANT, mRID: 'NEW', powerW: 7000 }] }) })
    }) as never)
    render(FleetPane)
    await waitFor(() => expect(calls).toHaveLength(1))
    await fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(screen.getByTestId('fleet-grant')).toHaveTextContent('7,000 W'))

    releaseFirst({ ok: true, data: commitments('AGGR', { grants: [{ ...GRANT, mRID: 'OLD', powerW: 1000 }] }) })
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(screen.getAllByTestId('fleet-grant')).toHaveLength(1)
    expect(screen.getByTestId('fleet-grant')).toHaveTextContent('7,000 W')
    expect(screen.getByTestId('fleet-grant')).not.toHaveTextContent('1,000 W')
  })

  it('renders every row when the route repeats an mRID', async () => {
    mockRoutes([fleet('AGGD2')], {
      AGGD2: {
        ok: true,
        data: commitments('AGGD2', {
          grants: [GRANT, { ...GRANT, powerW: 6000 }],
          plainControls: [
            { mRID: 'C', edevId: 'E', window: { start: 1_700_000_000, duration: 600 }, targetW: 1 },
            { mRID: 'C', edevId: 'E', window: { start: 1_700_000_000, duration: 600 }, targetW: 2 },
          ],
        }),
      },
    })
    render(FleetPane)
    expect(await screen.findAllByTestId('fleet-grant')).toHaveLength(2)
    expect(screen.getAllByTestId('fleet-control')).toHaveLength(2)
  })
})
