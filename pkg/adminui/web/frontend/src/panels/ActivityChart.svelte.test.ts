// The chart's library is bundled, not fetched, so mounting the panel with
// no network available at all must still produce an initialized chart.
// jsdom supplies no 2D canvas context, hence the stub; that the chart
// visibly DRAWS with outbound egress blocked is asserted in a real
// browser by e2e/chart_offline.spec.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/svelte'
import ActivityChart from './ActivityChart.svelte'
import * as echarts from 'echarts/core'
import { installCanvasStub } from '../test-canvas-stub'
import { css, darkToken } from '../test-tokens'

installCanvasStub()

let tokens: HTMLStyleElement
beforeEach(() => {
  tokens = document.createElement('style')
  tokens.textContent = css
  document.head.appendChild(tokens)
})
afterEach(() => tokens.remove())

describe('ActivityChart', () => {
  it('draws its three lines in the first three chart tokens and the chrome in the axis and grid tokens', async () => {
    const { container } = render(ActivityChart, {
      props: { history: [{ time: '00:00:05', devices: 2, mups: 1, commsOnline: 1 }] },
    })
    const mount = container.querySelector('#activityChart') as HTMLElement
    await waitFor(() => expect(echarts.getInstanceByDom(mount)?.getOption().series).toHaveLength(3))
    const option = echarts.getInstanceByDom(mount)!.getOption() as Record<string, any>
    const [reg, comms, mups] = option.series
    expect(reg.lineStyle.color).toBe(darkToken('--chart-1'))
    expect(reg.areaStyle.color).toBe(darkToken('--chart-area'))
    expect(comms.lineStyle.color).toBe(darkToken('--chart-2'))
    expect(mups.lineStyle.color).toBe(darkToken('--chart-3'))
    expect(option.xAxis[0].axisLabel.color).toBe(darkToken('--chart-axis'))
    expect(option.yAxis[0].splitLine.lineStyle.color).toBe(darkToken('--chart-grid'))
    expect(option.legend[0].textStyle.color).toBe(darkToken('--chart-axis'))
    expect(option.tooltip[0].backgroundColor).toBe(darkToken('--chart-tooltip-bg'))
  })

  it('initializes the bundled chart library with no network request of any kind', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')

    const { container } = render(ActivityChart, {
      props: { history: [{ time: '00:00:05', devices: 2, mups: 1, commsOnline: 1 }] },
    })

    const mount = container.querySelector('#activityChart') as HTMLElement
    expect(mount).not.toBeNull()
    await waitFor(() => expect(mount.querySelector('canvas')).not.toBeNull())
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(container.querySelector('[data-testid="chart-error"]')).toBeNull()
  })

  it('does not inject a script element pointing at any external origin', () => {
    render(ActivityChart, { props: { history: [] } })

    const external = Array.from(document.querySelectorAll('script[src]')).filter((s) =>
      /^(https?:)?\/\//.test(s.getAttribute('src') ?? ''),
    )
    expect(external).toEqual([])
  })
})
