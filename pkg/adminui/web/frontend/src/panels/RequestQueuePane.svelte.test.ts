// The queue pane against the #738 fixture. What it must get right: a 404
// reads as "not available" and not as an empty queue, a null quantity reads
// as missing and not 0, the tip is prominent with earlier answers as
// history, and the deadline counts down from the server's clock.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, within, cleanup } from '@testing-library/svelte'
import RequestQueuePane from './RequestQueuePane.svelte'
import * as api from '../lib/api'
import fixture from '../lib/flowreservation.fixture.json'

function mockOk(data: unknown) {
  return vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data } as never)
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  vi.setSystemTime(new Date(1_790_000_100 * 1000))
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('RequestQueuePane', () => {
  it('requests the flow-reservations route', async () => {
    const spy = mockOk(fixture)
    render(RequestQueuePane)
    await screen.findAllByTestId('frq-row')
    expect(spy).toHaveBeenCalledWith('/api/derms/flow-reservations')
  })

  it('shows not available on a 404, not an empty queue', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, error: 'not found', status: 404 } as never)
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

  it('shows an error for another failure', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, error: 'boom', status: 500 } as never)
    render(RequestQueuePane)
    expect(await screen.findByTestId('frq-error')).toHaveTextContent('boom')
  })

  it('renders the pending request with its values and direction in words', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const rows = await screen.findAllByTestId('frq-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByTestId('frq-state')).toHaveTextContent('pending')
    expect(within(rows[0]).getByTestId('frq-requested')).toHaveTextContent('10,000 Wh charge, 20,000 W')
    expect(within(rows[1]).getByTestId('frq-requested')).toHaveTextContent('8,000 Wh discharge, 8,000 W')
    expect(within(rows[1]).getByTestId('frq-state')).toHaveTextContent('granted')
  })

  it('counts the deadline down from the server clock', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const countdown = await screen.findByTestId('frq-countdown')
    expect(countdown).toHaveTextContent('200s to deadline')
    await vi.advanceTimersByTimeAsync(30_000)
    await waitFor(() => expect(countdown).toHaveTextContent('170s to deadline'))
    await vi.advanceTimersByTimeAsync(200_000)
    await waitFor(() => expect(countdown).toHaveTextContent('deadline passed'))
  })

  it('shows the tip prominently with commitment and its executing control', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    const tip = await screen.findByTestId('frq-tip')
    expect(within(tip).getByTestId('response-status')).toHaveTextContent('active')
    expect(within(tip).getByTestId('response-energy')).toHaveTextContent('4,000 Wh')
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
    const copy = JSON.parse(JSON.stringify(fixture))
    copy.requests[1].tip.energyRemainingWh = null
    copy.requests[1].tip.powerAvailable = null
    copy.requests[0].request.energyRequested = null
    mockOk(copy)
    render(RequestQueuePane)
    const tip = await screen.findByTestId('frq-tip')
    expect(within(tip).getByTestId('response-commitment')).toHaveTextContent('remaining missing')
    expect(within(tip).getByTestId('response-power')).toHaveTextContent('missing')
    expect(within(tip).getByTestId('response-power')).not.toHaveTextContent('0 W')
    const rows = screen.getAllByTestId('frq-row')
    expect(within(rows[0]).getByTestId('frq-requested')).toHaveTextContent('missing charge, 20,000 W')
  })

  it('has no answer, revise or cancel button', async () => {
    mockOk(fixture)
    render(RequestQueuePane)
    await screen.findAllByTestId('frq-row')
    const names = screen.getAllByRole('button').map((b) => b.textContent?.trim())
    expect(names).toEqual(['Refresh'])
  })

  it('stops its timer on unmount', async () => {
    mockOk(fixture)
    const { unmount } = render(RequestQueuePane)
    await screen.findAllByTestId('frq-row')
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
