// PowerCard owns the poll timer and reads through lib/power's kept answers.
// The tests drive a mocked fetchJSON under fake timers, stub the canvas, and
// unmount what they mount.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import * as echarts from 'echarts/core'
import PowerCard from './PowerCard.svelte'
import * as api from '../lib/api'
import { forgetPower } from '../lib/power'
import { wireAnswer } from '../lib/power.fixture'
import { installCanvasStub } from '../test-canvas-stub'

installCanvasStub()

beforeEach(() => {
  vi.useFakeTimers()
  forgetPower()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const chartSeries = () => {
  const el = screen.getByTestId('descriptor-chart') as HTMLElement
  const opt = echarts.getInstanceByDom(el)!.getOption() as { series: { name: string; data: [number, number][] }[] }
  return opt.series.map((s) => [s.name, s.data.map((p) => p[1])])
}

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
    expect(screen.getByTestId('power-energy-totals')).toHaveTextContent(
      'This window: discharged 0.5000 kWh, charged -0.2500 kWh',
    )
    expect(screen.queryByTestId('power-stale')).toBeNull()
    expect(screen.queryByTestId('power-error')).toBeNull()
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

  it('asks for one aggregator when it is chosen', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: wireAnswer({ aggregators: ['AGG1'] }) })
    const { unmount } = render(PowerCard)
    await vi.advanceTimersByTimeAsync(0)
    const select = screen.getByLabelText('Devices') as HTMLSelectElement
    select.value = 'AGG1'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await vi.advanceTimersByTimeAsync(0)

    expect(spy.mock.calls.at(-1)![0]).toBe('/api/derms/power?window=900&aggregatorLFDI=AGG1')
    unmount()
  })
})
