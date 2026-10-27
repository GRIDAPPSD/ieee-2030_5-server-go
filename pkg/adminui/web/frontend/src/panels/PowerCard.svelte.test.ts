// PowerCard owns the poll timer and reads through lib/power's kept answers.
// The tests drive a mocked fetchJSON under fake timers, stub the canvas, and
// unmount what they mount.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import * as echarts from 'echarts/core'
import PowerCard from './PowerCard.svelte'
import * as api from '../lib/api'
import { POWER_WINDOWS, forgetPower } from '../lib/power'
import { wireAnswer } from '../lib/power.fixture'
import { installCanvasStub } from '../test-canvas-stub'

import { useAppCss } from '../test-tokens'

installCanvasStub()
useAppCss()

beforeEach(() => {
  vi.useFakeTimers()
  forgetPower()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const chartOption = () => {
  const el = screen.getByTestId('descriptor-chart') as HTMLElement
  return echarts.getInstanceByDom(el)!.getOption() as { series: { name: string; type: string; data: [number, number][] }[] }
}
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
      'This window: discharged 0.5000 kWh, charged -0.2500 kWh',
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
    expect(spy).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(spy).toHaveBeenCalledTimes(2)
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
    expect(signal?.aborted).toBe(false)

    unmount()
    expect(signal?.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(5000 * 6)
    expect(spy).toHaveBeenCalledTimes(1)
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
    expect(first).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(60000)
    // The next read never settles, so anything drawn came from the kept answer.
    first.mockReset()
    const pending = first.mockReturnValue(new Promise(() => {}))
    const b = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(pending).toHaveBeenCalledTimes(1)
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
    expect(spy.mock.calls.map((c) => c[0])).toEqual([
      '/api/derms/power?window=900&scope=all',
      '/api/derms/power?window=3600&scope=all',
    ])
    expect(screen.getByTestId('power-now')).toHaveTextContent('9.0 kW')
    a.unmount()

    spy.mockReturnValue(new Promise(() => {}))
    const b = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    expect(spy.mock.calls.at(-1)![0]).toBe('/api/derms/power?window=3600&scope=all')
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
    expect(spy.mock.calls.map((c) => c[0])).toEqual(['/api/derms/power?window=900&scope=all'])
    unmount()
  })

  it('draws one discharge and one charge bar series per aggregator, Direct last, charge below zero', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ aggregatorEnergy: perAggregator }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)

    expect(chartSeries()).toEqual([
      ['North discharge', [0.5]],
      ['North charge', [-0.1]],
      ['South discharge', [0.25]],
      ['South charge', []],
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
    expect(list).toHaveTextContent('North 2.0 kW')
    expect(list).toHaveTextContent('South unavailable')
    expect(list).toHaveTextContent('Direct -0.5 kW')
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
})
