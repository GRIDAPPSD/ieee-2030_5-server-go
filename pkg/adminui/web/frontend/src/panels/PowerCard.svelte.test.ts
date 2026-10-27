// PowerCard owns the poll timer and reads through lib/power's kept answers.
// The tests drive a mocked fetchJSON under fake timers, stub the canvas, and
// unmount what they mount.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import * as echarts from 'echarts/core'
import PowerCard from './PowerCard.svelte'
import * as api from '../lib/api'
import { forgetLookAhead, rememberLookAhead } from '../lib/controlEvents'
import { POWER_WINDOWS, forgetPower } from '../lib/power'
import { wireAnswer } from '../lib/power.fixture'
import { installCanvasStub } from '../test-canvas-stub'

import { useAppCss } from '../test-tokens'

installCanvasStub()
useAppCss()

beforeEach(() => {
  vi.useFakeTimers()
  forgetPower()
  forgetLookAhead()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const chartOption = () => {
  const el = screen.getByTestId('descriptor-chart') as HTMLElement
  return echarts.getInstanceByDom(el)!.getOption() as { series: { name: string; type: string; data: [number, number][] }[] }
}
// The card's power reads only; the events timeline under it reads its own route.
const powerReads = (spy: { mock: { calls: unknown[][] } }) => spy.mock.calls.filter((c) => String(c[0]).startsWith('/api/derms/power'))

const chartSeries = () => chartOption().series.map((s) => [s.name, s.data.map((p) => p[1])])

describe('PowerCard', () => {
  it('draws the energy series, shows the kW and asks for all devices over 15 minutes', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer() })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(spy.mock.calls[0][0]).toBe('/api/derms/power?window=900&scope=all')
    expect(screen.getByTestId('power-now')).toHaveTextContent('2.0 kW')
    expect(chartSeries()).toEqual([
      ['Discharged', [0.5]],
      ['Charged', [-0.25]],
    ])
    expect(chartOption().series.map((s) => [s.name, s.type])).toEqual([
      ['Discharged', 'bar'],
      ['Charged', 'bar'],
    ])
    expect(screen.getByTestId('power-energy-totals')).toHaveTextContent(
      'This window: discharged 0.500 kWh, charged 0.250 kWh',
    )
    expect(screen.queryByTestId('power-stale')).toBeNull()
    expect(screen.queryByTestId('power-error')).toBeNull()
    unmount()
  })

  it('draws the chart frame with an empty message when the window holds no energy', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({
      ok: true,
      data: wireAnswer({ latestW: null, latestAt: null, energyChart: { unit: 'kWh', series: [] } }),
    })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    const el = screen.getByTestId('descriptor-chart') as HTMLElement
    const option = echarts.getInstanceByDom(el)!.getOption() as { xAxis: { type: string; min: number; max: number }[]; yAxis: { name: string }[] }
    expect(option.xAxis[0].type).toBe('time')
    expect(option.xAxis[0].max - option.xAxis[0].min).toBe(POWER_WINDOWS[0].seconds * 1000)
    expect(option.yAxis[0].name).toBe('kWh')
    expect(screen.getByTestId('descriptor-chart-empty')).toHaveTextContent('No energy readings in this window yet.')
    expect(screen.getByTestId('power-now-none')).toHaveTextContent('No reading yet')
    unmount()
  })

  it('shows a device by name with its LFDI as the hover text', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ notReporting: ['D4'] }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    const name = screen.getByTestId('power-not-reporting').querySelector('span')!
    expect(name).toHaveAttribute('title', 'D4')
    expect(name).toHaveTextContent('D4...')
    unmount()
  })

  it('names the missing devices when the kW is null, and shows no number', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({
      ok: true,
      data: wireAnswer({ latestW: null, latestAt: null, latestMissing: ['D2', 'D3'], notReporting: ['D4'] }),
    })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.queryByTestId('power-now')).toBeNull()
    expect(screen.getByTestId('power-now-missing')).toHaveTextContent('no recent reading from D2..., D3...')
    expect(screen.getByTestId('power-not-reporting')).toHaveTextContent('Not reporting: D4...')
    unmount()
  })

  it('lists a flagged device with the reason', async () => {
    const flagged = { lfdi: 'D1', drawn: true, coveredBuckets: 1, directionUnknown: true, concurrentMirrors: false, excludedReadings: 0, nonDERWatts: true }
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ devices: [flagged] }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.getByTestId('power-flagged')).toHaveTextContent('Flagged: D1... (direction unknown, watts on a non-DER mirror)')
    unmount()
  })

  it('reads again once per bucket the answer states', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ bucketSeconds: 5 }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(4999)
    expect(powerReads(spy)).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(powerReads(spy)).toHaveLength(2)
    unmount()
  })

  it('shows the sign-in message on a 401 and no chart', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, status: 401, error: 'admin authentication required' })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.getByTestId('power-error')).toHaveTextContent('Admin session required. Sign in again.')
    expect(screen.queryByTestId('descriptor-chart')).toBeNull()
    expect(screen.queryByTestId('power-loading')).toBeNull()
    unmount()
  })

  it('keeps the last chart, marked with its time, when a refresh fails', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer() })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    const at = new Date(Date.now()).toLocaleTimeString()

    spy.mockResolvedValue({ ok: false, status: 500, error: 'boom' })
    await vi.advanceTimersByTimeAsync(5000)

    expect(screen.getByTestId('power-error')).toHaveTextContent('Could not load the power card: boom')
    expect(screen.getByTestId('power-stale')).toHaveTextContent(`Showing the answer from ${at}.`)
    expect(chartSeries()[0]).toEqual(['Discharged', [0.5]])
    expect(screen.getByTestId('power-now')).toHaveTextContent('2.0 kW')

    spy.mockResolvedValue({ ok: true, data: wireAnswer({ latestW: 4000 }) })
    await vi.advanceTimersByTimeAsync(5000)
    expect(screen.queryByTestId('power-error')).toBeNull()
    expect(screen.queryByTestId('power-stale')).toBeNull()
    expect(screen.getByTestId('power-now')).toHaveTextContent('4.0 kW')
    unmount()
  })

  it('refuses an unreadable reply as an error and keeps the earlier chart', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer() })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    spy.mockResolvedValue({ ok: true, data: { latestW: 1 } })
    await vi.advanceTimersByTimeAsync(5000)

    expect(screen.getByTestId('power-error')).toHaveTextContent('The server sent a power reply that could not be read.')
    expect(screen.getByTestId('power-now')).toHaveTextContent('2.0 kW')
    unmount()
  })

  it('stops polling and aborts the read when unmounted', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer() })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    const signal = spy.mock.calls[0][1]?.signal
    const eventsSignal = spy.mock.calls[1][1]?.signal
    expect(signal?.aborted).toBe(false)
    expect(eventsSignal?.aborted).toBe(false)

    unmount()
    expect(signal?.aborted).toBe(true)
    expect(eventsSignal?.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(5000 * 6)
    expect(spy).toHaveBeenCalledTimes(2)
    expect(powerReads(spy)).toHaveLength(1)
  })

  it('does not read again when the read settles as cancelled after unmount', async () => {
    let settle!: (r: Awaited<ReturnType<typeof api.fetchJSON>>) => void
    const spy = vi.spyOn(api, 'fetchJSON').mockReturnValue(new Promise((resolve) => (settle = resolve)))
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    expect(spy).toHaveBeenCalledTimes(1)

    unmount()
    settle({ ok: false, status: 0, error: 'request cancelled' })
    await vi.advanceTimersByTimeAsync(5000 * 6)
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('shows no cancelled error for the new window when the old read settles as cancelled mid-change', async () => {
    const settles: ((r: Awaited<ReturnType<typeof api.fetchJSON>>) => void)[] = []
    vi.spyOn(api, 'fetchJSON').mockImplementation(() => new Promise((resolve) => settles.push(resolve)))
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    expect(settles).toHaveLength(1)

    await fireEvent.change(screen.getByLabelText('Window'), { target: { value: String(POWER_WINDOWS[1].seconds) } })
    await vi.advanceTimersByTimeAsync(0)
    expect(settles).toHaveLength(2)

    settles[0]({ ok: false, status: 0, error: 'request cancelled' })
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.queryByTestId('power-error')).toBeNull()
    expect(screen.getByTestId('power-loading')).toBeInTheDocument()
    unmount()
  })

  it('draws the previous chart at once on return, marked with its time, before the next read lands', async () => {
    const first = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer() })
    const a = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    const at = new Date(Date.now()).toLocaleTimeString()
    a.unmount()
    expect(powerReads(first)).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(60000)
    // The next read never settles, so anything drawn came from the kept answer.
    first.mockReset()
    const pending = first.mockReturnValue(new Promise(() => {}))
    const b = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(powerReads(pending)).toHaveLength(1)
    expect(chartSeries()).toEqual([
      ['Discharged', [0.5]],
      ['Charged', [-0.25]],
    ])
    expect(screen.getByTestId('power-now')).toHaveTextContent('2.0 kW')
    expect(screen.getByTestId('power-stale')).toHaveTextContent(`Showing the answer from ${at}.`)
    expect(screen.queryByTestId('power-loading')).toBeNull()
    b.unmount()
  })

  it('keeps one answer per scope and window and reopens on the last choice', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path) =>
      String(path).includes('window=3600')
        ? { ok: true, data: wireAnswer({ latestW: 9000 }) }
        : { ok: true, data: wireAnswer({ latestW: 1000 }) },
    )
    const a = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    const select = screen.getByLabelText('Window') as HTMLSelectElement
    select.value = '3600'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await vi.advanceTimersByTimeAsync(0)
    expect(powerReads(spy).map((c) => c[0])).toEqual([
      '/api/derms/power?window=900&scope=all',
      '/api/derms/power?window=3600&scope=all',
    ])
    expect(screen.getByTestId('power-now')).toHaveTextContent('9.0 kW')
    a.unmount()

    spy.mockReturnValue(new Promise(() => {}))
    const b = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    expect(powerReads(spy).at(-1)![0]).toBe('/api/derms/power?window=3600&scope=all')
    expect(screen.getByTestId('power-now')).toHaveTextContent('9.0 kW')
    b.unmount()
  })

  const perAggregator = [
    { key: 'AGG1', name: 'North', latestW: 2000, latestMissing: [], discharged: [[1000, 0.5]], charged: [[1000, 0.1]] },
    { key: 'AGG2', name: 'South', latestW: null, latestMissing: ['D9'], discharged: [[1000, 0.25]], charged: [[1000, null]] },
    { key: 'direct', name: 'Direct', latestW: -500, latestMissing: [], discharged: [[1000, null]], charged: [[1000, 0.3]] },
  ]

  it('has no device picker and always asks for the whole fleet', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ aggregators: ['AGG1'] }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.queryByLabelText('Devices')).toBeNull()
    expect(powerReads(spy).map((c) => c[0])).toEqual(['/api/derms/power?window=900&scope=all'])
    unmount()
  })

  it('draws one discharge and one charge bar series per aggregator, Direct last, charge below zero', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ aggregatorEnergy: perAggregator }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(chartSeries()).toEqual([
      ['AGG1... discharge', [0.5]],
      ['AGG1... charge', [-0.1]],
      ['AGG2... discharge', [0.25]],
      ['AGG2... charge', []],
      ['Direct discharge', []],
      ['Direct charge', [-0.3]],
    ])
    expect(chartOption().series.every((s) => s.type === 'bar')).toBe(true)
    // Bucket starts arrive in seconds and are drawn in milliseconds.
    expect(chartOption().series[0].data[0][0]).toBe(1_000_000)
    unmount()
  })

  it('lists power now per aggregator beside the total', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ aggregatorEnergy: perAggregator }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.getByTestId('power-now')).toHaveTextContent('2.0 kW')
    const list = screen.getByTestId('power-by-aggregator')
    expect(list).toHaveTextContent('AGG1... 2.0 kW')
    expect(list).toHaveTextContent('AGG2... unavailable')
    expect(list).toHaveTextContent('Direct -0.5 kW')
    unmount()
  })

  it('labels an aggregator by its device label when the wire name is the LFDI, with the LFDI on hover', async () => {
    const lfdi = 'AABBCCDDEEFF00112233'
    const entries = [
      { key: lfdi, name: lfdi, latestW: 1000, latestMissing: [], discharged: [[1000, 0.5]], charged: [[1000, 0.1]] },
      { key: 'direct', name: 'Direct', latestW: null, latestMissing: [], discharged: [[1000, null]], charged: [[1000, null]] },
    ]
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ aggregatorEnergy: entries }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(chartSeries().map((s) => s[0])).toEqual(['AABBCCDDEEFF0011... discharge', 'AABBCCDDEEFF0011... charge', 'Direct discharge', 'Direct charge'])
    const item = screen.getByTestId('power-by-aggregator').querySelector('li')!
    expect(item).toHaveAttribute('title', lfdi)
    expect(item).toHaveTextContent('AABBCCDDEEFF0011... 1.0 kW')
    unmount()
  })

  it('draws the single total series when the per-aggregator list is empty', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ aggregatorEnergy: [] }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(chartSeries()).toEqual([
      ['Discharged', [0.5]],
      ['Charged', [-0.25]],
    ])
    expect(screen.queryByTestId('power-by-aggregator')).toBeNull()
    unmount()
  })

  it('draws the single total series when the server sends no per-aggregator energy', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer() })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(chartSeries().map((s) => s[0])).toEqual(['Discharged', 'Charged'])
    expect(screen.queryByTestId('power-by-aggregator')).toBeNull()
    unmount()
  })

  it('fixes the time axis to the window and gives the empty frame a value axis', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({
      ok: true,
      data: wireAnswer({ energyChart: { unit: 'kWh', series: [] } }),
    })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    const el = screen.getByTestId('descriptor-chart') as HTMLElement
    const o = echarts.getInstanceByDom(el)!.getOption() as { yAxis: { min: number; max: number }[] }
    expect([o.yAxis[0].min, o.yAxis[0].max]).toEqual([-1, 1])
    // No energy: the totals line is kept out of sight, not shown as zeros.
    expect(screen.getByTestId('power-energy-totals')).toHaveClass('visually-hidden')
    unmount()
  })

  it('shows the totals line once the window holds energy', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer() })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.getByTestId('power-energy-totals')).not.toHaveClass('visually-hidden')
    unmount()
  })

  describe('look-ahead', () => {
    const answerAtNow = () => {
      const t = Date.now() - 60000
      return wireAnswer({
        energyChart: { unit: 'kWh', series: [{ name: 'Discharged', points: [[t, 0.5]] }, { name: 'Charged', points: [] }] },
      })
    }
    const serve = (events: Record<string, unknown>) =>
      vi.spyOn(api, 'fetchJSON').mockImplementation(async (path) =>
        String(path).startsWith('/api/derms/control-events')
          ? { ok: true, data: events }
          : { ok: true, data: answerAtNow() },
      )
    const axis = () => {
      const el = screen.getByTestId('descriptor-chart') as HTMLElement
      const o = echarts.getInstanceByDom(el)!.getOption() as { xAxis: { min: number; max: number }[] }
      return o.xAxis[0]
    }

    it('extends both charts to the look-ahead end, marks now on both, and stops the power bars at now', async () => {
      const win = POWER_WINDOWS[0].seconds
      serve({ windowStart: 0, windowEnd: win, aheadEnd: win * 2, events: [] })
      const { unmount, container } = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)

      const x = axis()
      expect(x.max - x.min).toBe(win * 2 * 1000)
      const now = x.min + win * 1000
      expect(Date.now()).toBe(now)
      expect(screen.getByTestId('chart-now').style.left).toBe('50%')
      expect(container.querySelector<HTMLElement>('.ev-now')!.style.left).toBe('50%')
      const lastBar = Math.max(...chartOption().series.flatMap((s) => s.data.map((p) => p[0])))
      expect(lastBar).toBeLessThanOrEqual(now)
      unmount()
    })

    it('draws the chart grid at the --plot margins the timeline lines up by', async () => {
      serve({ windowStart: 0, windowEnd: 900, aheadEnd: 1800, events: [] })
      const { unmount } = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)
      const el = screen.getByTestId('descriptor-chart') as HTMLElement
      const o = echarts.getInstanceByDom(el)!.getOption() as { grid: Record<string, number>[] }
      expect([o.grid[0].left, o.grid[0].right, o.grid[0].top, o.grid[0].bottom]).toEqual([60, 30, 56, 30])
      unmount()
    })

    it('sets the look-ahead at once on a window change and on return, so the range does not rescale', async () => {
      serve({ windowStart: 0, windowEnd: 900, aheadEnd: 1800, events: [] })
      const a = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)
      expect(axis().max - axis().min).toBe(900 * 2 * 1000)

      // The events read for the new window never answers.
      vi.spyOn(api, 'fetchJSON').mockImplementation(async (path) =>
        String(path).startsWith('/api/derms/control-events') ? new Promise(() => {}) : { ok: true, data: answerAtNow() },
      )
      await fireEvent.change(screen.getByLabelText('Window'), { target: { value: '3600' } })
      await vi.advanceTimersByTimeAsync(0)
      expect(axis().max - axis().min).toBe(3600 * 2 * 1000)
      a.unmount()

      const b = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)
      expect(axis().max - axis().min).toBe(3600 * 2 * 1000)
      b.unmount()
    })

    it('keeps no look-ahead on a window change once the route answered 404', async () => {
      vi.spyOn(api, 'fetchJSON').mockImplementation(async (path) =>
        String(path).startsWith('/api/derms/control-events') ? { ok: false, status: 404, error: 'nf' } : { ok: true, data: answerAtNow() },
      )
      const { unmount } = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)
      expect(axis().max - axis().min).toBe(900 * 1000)
      await fireEvent.change(screen.getByLabelText('Window'), { target: { value: '3600' } })
      await vi.advanceTimersByTimeAsync(0)
      expect(axis().max - axis().min).toBe(3600 * 1000)
      unmount()
    })

    it('drops the look-ahead when a server that had one answers 404', async () => {
      rememberLookAhead(true)
      vi.spyOn(api, 'fetchJSON').mockImplementation(async (path) =>
        String(path).startsWith('/api/derms/control-events') ? { ok: false, status: 404, error: 'nf' } : { ok: true, data: answerAtNow() },
      )
      const { unmount } = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)
      expect(axis().max - axis().min).toBe(900 * 1000)
      unmount()
    })

    it('ignores a look-ahead answer that lands after the window changed', async () => {
      const settles: ((r: Awaited<ReturnType<typeof api.fetchJSON>>) => void)[] = []
      vi.spyOn(api, 'fetchJSON').mockImplementation((path) =>
        String(path).startsWith('/api/derms/control-events')
          ? new Promise((resolve) => settles.push(resolve))
          : Promise.resolve({ ok: true, data: answerAtNow() }),
      )
      const { unmount } = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)
      await fireEvent.change(screen.getByLabelText('Window'), { target: { value: '3600' } })
      await vi.advanceTimersByTimeAsync(0)
      expect(settles.length).toBeGreaterThan(1)

      // The read for the old window settles late, with a look-ahead.
      settles[0]({ ok: true, data: { windowStart: 0, windowEnd: 900, aheadEnd: 900 + 99999, events: [] } })
      await vi.advanceTimersByTimeAsync(0)
      expect(axis().max - axis().min).toBe(3600 * 1000)
      unmount()
    })

    it('ends at now when the server sends no aheadEnd', async () => {
      const win = POWER_WINDOWS[0].seconds
      serve({ windowStart: 0, windowEnd: win, events: [] })
      const { unmount } = render(PowerCard)
      await vi.advanceTimersByTimeAsync(0)

      const x = axis()
      expect(x.max - x.min).toBe(win * 1000)
      expect(screen.queryByTestId('chart-ahead')).toBeNull()
      unmount()
    })
  })
})
