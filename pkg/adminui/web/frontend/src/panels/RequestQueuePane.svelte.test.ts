// The queue pane against the fixture. What it must get right: a 404 reads
// as "not available" and not as an empty queue; a body the pane cannot
// render is a visible error with Refresh enabled, never a stuck "Loading";
// a null quantity reads as missing and not 0; the wire sign is shown; the
// deadline counts down from the server's clock; and a failed refresh keeps
// the last good queue, marked stale.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, within, cleanup } from '@testing-library/svelte'
import { tick } from 'svelte'
import RequestQueuePane from './RequestQueuePane.svelte'
import * as api from '../lib/api'
import fixture from '../lib/flowreservation.fixture.json'

type Res = { ok: true; data: unknown; serverTime?: number } | { ok: false; error: string; status: number }

const LFDI = fixture.aggregatorLFDI
const FLEETS: Res = { ok: true, data: [{ aggregatorLFDI: LFDI }] }

// copy returns a deep copy of the fixture the test can edit.
function copy() {
  return JSON.parse(JSON.stringify(fixture))
}

function mockRoutes(flow: (path: string) => Res, fleets: Res = FLEETS) {
  return vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) =>
    path === '/api/derms/fleets' ? fleets : flow(path)) as never)
}

function mockOk(data: unknown, serverTime?: number) {
  return mockRoutes(() => ({ ok: true, data, serverTime }))
}

async function loaded() {
  return screen.findAllByTestId('frq-row')
}

// The browser clock is deliberately far from the fixture's server clock
// (1790000100), so a countdown taken from it cannot match.
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  vi.setSystemTime(new Date(1_700_000_000 * 1000))
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('RequestQueuePane routes', () => {
  it('asks the fleet list, then the flow route once per aggregator with its LFDI', async () => {
    const other = 'AB'.repeat(20)
    const spy = mockRoutes(() => ({ ok: true, data: fixture }), {
      ok: true,
      data: [{ aggregatorLFDI: LFDI }, { aggregatorLFDI: other }],
    })
    render(RequestQueuePane)
    await loaded()
    const paths = spy.mock.calls.map((c) => c[0])
    expect(paths).toEqual([
      '/api/derms/fleets',
      '/api/derms/flow-reservations?aggregatorLFDI=' + LFDI,
      '/api/derms/flow-reservations?aggregatorLFDI=' + other,
    ])
  })

  it('bounds every fetch with a timeout and a signal', async () => {
    const spy = mockOk(fixture)
    render(RequestQueuePane)
    await loaded()
    for (const call of spy.mock.calls) {
      expect(call[1]).toMatchObject({ timeoutMs: 15_000 })
      expect(call[1]?.signal).toBeInstanceOf(AbortSignal)
    }
  })

  it('aborts its requests on unmount and stops both timers', async () => {
    const spy = mockOk(fixture)
    const { unmount } = render(RequestQueuePane)
    await loaded()
    expect(vi.getTimerCount()).toBe(2)
    const signal = spy.mock.calls[0][1]!.signal!
    expect(signal.aborted).toBe(false)
    unmount()
    expect(signal.aborted).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('RequestQueuePane states', () => {
  it('shows not available on a 404, not an empty queue', async () => {
    mockRoutes(() => ({ ok: false, error: 'not found', status: 404 }))
    render(RequestQueuePane)
    expect(await screen.findByTestId('frq-unavailable')).toHaveTextContent('not available on this server yet')
    expect(screen.queryByTestId('frq-empty')).toBeNull()
  })

  it('shows an empty queue only for a 200 with no requests', async () => {
    mockOk({ ...fixture, requests: [] })
    render(RequestQueuePane)
    expect(await screen.findByTestId('frq-empty')).toBeInTheDocument()
    expect(screen.queryByTestId('frq-unavailable')).toBeNull()
  })

  it('shows an error for another failure, with Refresh enabled', async () => {
    mockRoutes(() => ({ ok: false, error: 'boom', status: 500 }))
    render(RequestQueuePane)
    expect(await screen.findByTestId('frq-error')).toHaveTextContent('boom')
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })

  it('ends a hung fetch in the error state with Refresh enabled', async () => {
    vi.useRealTimers()
    vi.useFakeTimers()
    vi.stubGlobal(
      'fetch',
      (_path: string, init: RequestInit) =>
        new Promise((_resolve, reject) => {
          init.signal?.addEventListener('abort', () => reject(new Error('aborted')))
        }),
    )
    render(RequestQueuePane)
    await tick()
    expect(screen.getByTestId('frq-loading')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeDisabled()
    await vi.advanceTimersByTimeAsync(15_000)
    await tick()
    expect(screen.getByTestId('frq-error')).toHaveTextContent('request timed out')
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })
})

describe('RequestQueuePane malformed bodies', () => {
  async function expectErrorState(data: unknown, text: string) {
    mockOk(data)
    render(RequestQueuePane)
    const err = await screen.findByTestId('frq-error')
    expect(err).toHaveTextContent(text)
    expect(screen.queryByTestId('frq-loading')).toBeNull()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  }

  it('missing responses list', async () => {
    const d = copy()
    delete d.requests[0].responses
    await expectErrorState(d, 'has no responses list')
  })

  it('duplicate requestHref', async () => {
    const d = copy()
    d.requests[1].requestHref = d.requests[0].requestHref
    await expectErrorState(d, 'repeats request')
  })

  it('duplicate response id', async () => {
    const d = copy()
    d.requests[1].responses[1].id = d.requests[1].responses[0].id
    await expectErrorState(d, 'repeats response')
  })

  it('a request that is not an object', async () => {
    const d = copy()
    d.requests[0] = null
    await expectErrorState(d, 'is not an object')
  })

  it('a missing request body', async () => {
    const d = copy()
    delete d.requests[0].request
    await expectErrorState(d, 'has no request')
  })

  it('a body that is not a queue', async () => {
    await expectErrorState(null, 'queue is not an object')
  })

  it('a null interval renders as missing instead of throwing', async () => {
    const d = copy()
    d.requests[0].request.intervalRequested = null
    d.requests[1].responses[0].interval = null
    mockOk(d)
    render(RequestQueuePane)
    const rows = await loaded()
    expect(rows[0]).toHaveTextContent('interval missing')
    expect(within(screen.getByTestId('frq-history')).getByText(/interval missing/)).toBeInTheDocument()
  })

  it('an undefined tip reads as no tip', async () => {
    const d = copy()
    delete d.requests[1].tip
    mockOk(d)
    render(RequestQueuePane)
    await loaded()
    expect(screen.queryByTestId('frq-tip')).toBeNull()
    expect(screen.getAllByTestId('frq-history')).toHaveLength(2)
  })
})

describe('RequestQueuePane content', () => {
  it('renders the pending request with its values and direction in words', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const rows = await loaded()
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByTestId('frq-state')).toHaveTextContent('pending')
    expect(within(rows[0]).getByTestId('frq-requested')).toHaveTextContent('10,000 Wh charge, 20,000 W')
    expect(within(rows[1]).getByTestId('frq-state')).toHaveTextContent('granted')
  })

  it('shows the wire sign: discharge energy stays negative', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const rows = await loaded()
    expect(within(rows[1]).getByTestId('frq-requested')).toHaveTextContent('-8,000 Wh discharge, 8,000 W')
    expect(within(screen.getByTestId('frq-tip')).getByTestId('response-energy')).toHaveTextContent('-4,000 Wh')
  })

  it('with no direction word the sign is the only direction and is shown', async () => {
    const d = copy()
    d.requests[1].request.direction = null
    mockOk(d)
    render(RequestQueuePane)
    const rows = await loaded()
    expect(within(rows[1]).getByTestId('frq-requested')).toHaveTextContent('-8,000 Wh direction missing')
  })

  it('shows an execution target signed, with the DER frame label', async () => {
    const d = copy()
    d.requests[1].tip.executions[0].targetW.value = -4000
    mockOk(d)
    render(RequestQueuePane)
    await loaded()
    expect(screen.getByTestId('response-execution')).toHaveTextContent('target -4,000 W discharge (DER frame)')
  })

  it('shows the tip prominently with commitment and its executing control', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const tip = await screen.findByTestId('frq-tip')
    expect(within(tip).getByTestId('response-status')).toHaveTextContent('active')
    expect(within(tip).getByTestId('response-commitment')).toHaveTextContent(
      'energy committed 1,000 Wh, remaining 3,000 Wh',
    )
    expect(within(tip).getByTestId('response-execution')).toHaveTextContent('control active')
    expect(within(tip).getByTestId('response-execution')).toHaveTextContent('target 4,000 W discharge (DER frame)')
    expect(within(tip).getByTestId('response-answered')).toHaveTextContent('answered by operator')
  })

  it('shows the earlier answer as cancelled history with who answered and cancelled', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const history = await screen.findByTestId('frq-history')
    expect(within(history).getByTestId('response-status')).toHaveTextContent('cancelled')
    expect(within(history).getByTestId('response-answered')).toHaveTextContent('deadline fallback')
    expect(within(history).getByTestId('response-cancelled')).toHaveTextContent(
      'cancelled by operator cert:5D9A0C1B7E3F2A4... via mtls: revised for feeder limit',
    )
    expect(within(history).queryByTestId('response-commitment')).toBeNull()
  })

  it('shows a null quantity as missing, never as 0', async () => {
    const d = copy()
    d.requests[1].tip.energyRemainingWh = null
    d.requests[1].tip.powerAvailable = null
    d.requests[0].request.energyRequested = null
    mockOk(d)
    render(RequestQueuePane)
    const tip = await screen.findByTestId('frq-tip')
    expect(within(tip).getByTestId('response-commitment')).toHaveTextContent('remaining missing')
    expect(within(tip).getByTestId('response-power')).toHaveTextContent('missing')
    const rows = screen.getAllByTestId('frq-row')
    expect(within(rows[0]).getByTestId('frq-requested')).toHaveTextContent('missing charge, 20,000 W')
  })

  it('shows a missing deadline and a missing multiplier as missing, never NaN', async () => {
    const d = copy()
    delete d.requests[0].deadlineAt
    delete d.requests[0].request.powerRequested.multiplier
    mockOk(d)
    const { container } = render(RequestQueuePane)
    const rows = await loaded()
    expect(within(rows[0]).getByTestId('frq-countdown')).toHaveTextContent('deadline missing')
    expect(within(rows[0]).getByTestId('frq-requested')).toHaveTextContent('10,000 Wh charge, missing')
    expect(container.textContent).not.toContain('NaN')
  })

  it('has no answer, revise or cancel button', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    await loaded()
    expect(screen.getAllByRole('button').map((b) => b.textContent?.trim())).toEqual(['Refresh'])
  })
})

describe('RequestQueuePane countdown', () => {
  it('counts down from the payload now, not the browser clock', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const countdown = await screen.findByTestId('frq-countdown')
    expect(countdown).toHaveTextContent('200s to deadline')
    await vi.advanceTimersByTimeAsync(20_000)
    await tick()
    expect(countdown).toHaveTextContent('180s to deadline')
  })

  it('uses the response Date header when the payload has no now', async () => {
    const d = copy()
    delete d.now
    mockOk(d, 1790000100 * 1000)
    render(RequestQueuePane)
    expect(await screen.findByTestId('frq-countdown')).toHaveTextContent('200s to deadline')
  })

  it('says server time is unavailable when there is neither', async () => {
    const d = copy()
    delete d.now
    mockOk(d)
    render(RequestQueuePane)
    expect(await screen.findByTestId('frq-countdown')).toHaveTextContent('server time unavailable')
  })

  it('reads a passed deadline as passed', async () => {
    const d = copy()
    d.requests[0].deadlineAt = d.now + 5
    mockOk(d)
    render(RequestQueuePane)
    const countdown = await screen.findByTestId('frq-countdown')
    await vi.advanceTimersByTimeAsync(10_000)
    await tick()
    expect(countdown).toHaveTextContent('deadline passed')
  })
})

describe('RequestQueuePane refresh', () => {
  it('shows the fetch age, and refetches on an interval', async () => {
    const spy = mockOk(fixture)
    render(RequestQueuePane)
    await loaded()
    expect(screen.getByTestId('frq-age')).toHaveTextContent('fetched 0s ago')
    await vi.advanceTimersByTimeAsync(20_000)
    await tick()
    expect(screen.getByTestId('frq-age')).toHaveTextContent('fetched 20s ago')
    expect(spy).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(10_000)
    await tick()
    expect(spy).toHaveBeenCalledTimes(4)
    expect(screen.getByTestId('frq-age')).toHaveTextContent('fetched 0s ago')
  })

  it('keeps the old queue visible, marked stale, when a refresh fails', async () => {
    let fail = false
    mockRoutes(() => (fail ? { ok: false, error: 'boom', status: 500 } : { ok: true, data: fixture }))
    render(RequestQueuePane)
    await loaded()
    fail = true
    await vi.advanceTimersByTimeAsync(30_000)
    await tick()
    const stale = screen.getByTestId('frq-stale')
    expect(stale).toHaveTextContent('boom')
    expect(stale).toHaveTextContent('stale')
    expect(screen.getAllByTestId('frq-row')).toHaveLength(2)
    expect(screen.queryByTestId('frq-error')).toBeNull()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })

  it('clears the stale mark when a later refresh succeeds', async () => {
    let fail = true
    mockRoutes(() => (fail ? { ok: false, error: 'boom', status: 500 } : { ok: true, data: fixture }))
    render(RequestQueuePane)
    await vi.advanceTimersByTimeAsync(0)
    await tick()
    expect(screen.getByTestId('frq-error')).toBeInTheDocument()
    fail = false
    await vi.advanceTimersByTimeAsync(30_000)
    await tick()
    expect(screen.getAllByTestId('frq-row')).toHaveLength(2)
    expect(screen.queryByTestId('frq-stale')).toBeNull()
    expect(screen.queryByTestId('frq-error')).toBeNull()
  })
})
