// EventsTimeline owns its poll timer and reads the card's range. The tests
// drive a mocked fetchJSON under fake timers and unmount what they mount.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import EventsTimeline from './EventsTimeline.svelte'
import * as api from '../lib/api'
import { END, wireEvent, wireEvents } from '../lib/controlEvents.fixture'
import { parsePower, windowRange, type PowerAnswer } from '../lib/power'
import { css } from '../test-tokens'
import { wireAnswer } from '../lib/power.fixture'
import { useAppCss } from '../test-tokens'

useAppCss()

const energy = (key: string) => ({ key, name: key, latestW: null, latestMissing: [], discharged: [], charged: [] })
const answer = (): PowerAnswer => {
  const a = parsePower(wireAnswer({ aggregatorEnergy: [energy('AGG1'), energy('AGG2'), energy('direct')] }))
  if (a === null) throw new Error('fixture did not parse')
  return a
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(END * 1000)
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

// The card's range reaches an hour past now once the events answer says so;
// the fixture's default look-ahead is that hour.
const mount = (ahead = 3600) =>
  render(EventsTimeline, { props: { answer: answer(), windowSeconds: 3600, range: windowRange(END * 1000, 3600, ahead), nowMs: END * 1000 } })
const reply = (data: unknown) => vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data })

describe('EventsTimeline', () => {
  it('draws a lane per aggregator then Direct, in the power chart order', async () => {
    reply(wireEvents([]))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getAllByTestId('events-lane-label').map((e) => e.textContent)).toEqual(['AGG1...', 'AGG2...', 'Direct'])
    unmount()
  })

  it('draws each event as a bar in its own lane with start, end and a label in words', async () => {
    reply(
      wireEvents([
        wireEvent({ group: 'AGG1', mrid: 'a', start: END - 1800, end: END - 900 }),
        wireEvent({ group: 'direct', groupKind: 'direct', mrid: 'b', direction: 'charge', targetW: -3000, start: END - 3600, end: END - 1800 }),
      ]),
    )
    const { unmount } = mount(0)
    await vi.advanceTimersByTimeAsync(0)
    const lanes = screen.getAllByTestId('events-lane')
    const a = within(lanes[0]).getByTestId('events-bar')
    expect(a).toHaveTextContent('discharge 5.0 kW')
    expect(a.style.left).toBe('50%')
    expect(a.style.width).toBe('25%')
    expect(within(lanes[1]).queryAllByTestId('events-bar')).toHaveLength(0)
    const b = within(lanes[2]).getByTestId('events-bar')
    expect(b).toHaveTextContent('charge 3.0 kW')
    expect(b.style.left).toBe('0%')
    expect(b.style.width).toBe('50%')
    expect(screen.queryByTestId('events-empty')).toBeNull()
    unmount()
  })

  it('marks a DERMS event and no admin one', async () => {
    reply(wireEvents([wireEvent({ mrid: 'a', origin: 'derms' }), wireEvent({ mrid: 'b', origin: 'admin', group: 'AGG2' })]))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    const bars = screen.getAllByTestId('events-bar')
    expect(bars.map((b) => [b.dataset.origin, within(b).queryAllByTestId('events-derms').length])).toEqual([
      ['derms', 1],
      ['admin', 0],
    ])
    expect(within(bars[0]).getByTestId('events-derms')).toHaveTextContent('DERMS')
    unmount()
  })

  it('styles each status distinctly and names it in the hover text', async () => {
    const statuses = ['scheduled', 'active', 'completed', 'cancelled', 'superseded'] as const
    reply(wireEvents(statuses.map((status, i) => wireEvent({ status, mrid: `m${i}`, start: END - 3000 + i * 100, end: END - 1000 }))))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    const bars = screen.getAllByTestId('events-bar')
    expect(bars.map((b) => b.dataset.status)).toEqual([...statuses])
    for (const [i, s] of statuses.entries()) {
      expect(bars[i]).toHaveClass(`ev-${s}`)
      expect(bars[i].title).toContain(`, ${s},`)
    }
    unmount()
  })

  it('shows the lanes and a short message when the window holds no events', async () => {
    reply(wireEvents([]))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByTestId('events-empty')).toHaveTextContent('No scheduled events in this window.')
    expect(screen.getAllByTestId('events-lane')).toHaveLength(3)
    unmount()
  })

  it('says nothing about emptiness before the first answer', async () => {
    vi.spyOn(api, 'fetchJSON').mockReturnValue(new Promise(() => {}))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.queryByTestId('events-empty')).toBeNull()
    expect(screen.getAllByTestId('events-lane')).toHaveLength(3)
    unmount()
  })

  it('hides the chart and stops asking on a 404 from an older server', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, status: 404, error: 'not found' })
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.queryByTestId('events-timeline')).toBeNull()
    await vi.advanceTimersByTimeAsync(5000 * 12)
    expect(spy).toHaveBeenCalledTimes(1)
    unmount()
  })

  it('keeps the last events and says so when a refresh fails', async () => {
    const spy = reply(wireEvents([wireEvent()]))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    spy.mockResolvedValue({ ok: false, status: 500, error: 'boom' })
    await vi.advanceTimersByTimeAsync(30000)
    expect(screen.getByTestId('events-error')).toHaveTextContent('Could not load scheduled events: boom')
    expect(screen.getAllByTestId('events-bar')).toHaveLength(1)
    unmount()
  })

  it('polls once per bucket and aborts its read on unmount', async () => {
    const spy = reply(wireEvents([]))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    expect(spy.mock.calls[0][0]).toBe('/api/derms/control-events?window=3600&scope=all')
    const signal = spy.mock.calls[0][1]?.signal
    expect(signal?.aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(4999)
    expect(spy).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(spy).toHaveBeenCalledTimes(2)
    unmount()
    expect(signal?.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(5000 * 6)
    expect(spy).toHaveBeenCalledTimes(2)
  })

  it('draws the now line at half the range with an hour of look-ahead, and at the end without', async () => {
    reply(wireEvents([]))
    const a = mount()
    await vi.advanceTimersByTimeAsync(0)
    const lines = a.container.querySelectorAll<HTMLElement>('.ev-now')
    expect(lines).toHaveLength(3)
    expect(lines[0].style.left).toBe('50%')
    expect(a.container.querySelector<HTMLElement>('.ev-future')!.style.left).toBe('50%')
    a.unmount()

    const b = mount(0)
    await vi.advanceTimersByTimeAsync(0)
    expect(b.container.querySelector<HTMLElement>('.ev-now')!.style.left).toBe('100%')
    b.unmount()
  })

  it('draws a scheduled bar that starts after now to the right of the now line', async () => {
    reply(wireEvents([wireEvent({ status: 'scheduled', direction: 'charge', targetW: -3000, start: END + 600, end: END + 1800 })]))
    const { unmount, container } = mount()
    await vi.advanceTimersByTimeAsync(0)
    const bar = screen.getByTestId('events-bar')
    const nowLeft = parseFloat(container.querySelector<HTMLElement>('.ev-now')!.style.left)
    expect(bar.dataset.status).toBe('scheduled')
    expect(parseFloat(bar.style.left)).toBeGreaterThan(nowLeft)
    expect(bar.style.left).toBe('58.333333333333336%')
    expect(bar.style.width).toBe('16.666666666666664%')
    unmount()
  })

  it('splits each label so a narrow bar can drop the target, and the stylesheet drops it under a width', async () => {
    reply(wireEvents([wireEvent()]))
    const { unmount } = mount()
    await vi.advanceTimersByTimeAsync(0)
    const bar = screen.getByTestId('events-bar')
    expect(bar.querySelector('.ev-detail')).toHaveTextContent('5.0 kW')
    expect(bar.querySelector('.ev-text')!.firstChild!.textContent).toBe('discharge')
    expect(bar).toHaveTextContent('discharge 5.0 kW')
    expect(css).toMatch(/@container \(max-width: 140px\)\s*\{\s*\.ev-detail\s*\{\s*display: none;/)
    unmount()
  })
})
