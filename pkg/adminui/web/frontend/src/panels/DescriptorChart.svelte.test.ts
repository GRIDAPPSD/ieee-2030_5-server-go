// The chart section. The hostile series name comes from the shared Go
// fixture, so the encoder and the renderer are tested with the same string.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/svelte'
import * as echarts from 'echarts/core'
import DescriptorChart from './DescriptorChart.svelte'
import DescriptorPanel from './DescriptorPanel.svelte'
import { installCanvasStub } from '../test-canvas-stub'
import type { Descriptor, DescriptorChartBody, DrawnChartBody } from '../lib/descriptor'

installCanvasStub()

// Passes through to the real library; one test makes init throw once.
vi.mock('echarts/core', async (importOriginal) => {
  const real = await importOriginal<typeof import('echarts/core')>()
  return { ...real, init: vi.fn(real.init) }
})

const fixture = JSON.parse(
  readFileSync('../../../../pkg/sep2admin/testdata/descriptor_v2.json', 'utf8'),
) as Descriptor
const chartSection = fixture.sections.find((s) => s.kind === 'chart')!
const body = chartSection.body as DescriptorChartBody
const hostile = '<img src=x onerror=alert(1)>'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function mounted(over: Partial<DrawnChartBody> = {}) {
  const view = render(DescriptorChart, { props: { body: { ...body, ...over }, empty: 'No samples yet.' } })
  const el = (await screen.findByTestId('descriptor-chart')) as HTMLElement
  await waitFor(() => expect(echarts.getInstanceByDom(el)).toBeDefined())
  return { ...view, el, instance: echarts.getInstanceByDom(el)! }
}

describe('DescriptorChart', () => {
  it('draws one line per series on a time axis, with the unit on the value axis and a legend', async () => {
    const { instance } = await mounted()
    await waitFor(() => expect(instance.getOption().series).toHaveLength(body.series.length))
    const option = instance.getOption() as {
      series: { name: string; type: string; data: unknown }[]
      xAxis: { type: string }[]
      yAxis: { name: string }[]
      legend: unknown[]
    }
    expect(option.series.map((s) => [s.name, s.type])).toEqual(body.series.map((s) => [s.name, 'line']))
    expect(option.series[0].data).toEqual(body.series[0].points)
    expect(option.xAxis[0].type).toBe('time')
    expect(option.yAxis[0].name).toBe(body.unit)
    expect(option.legend).toHaveLength(1)
  })

  it('draws a series typed bar as a stacked translucent bar with its direction colors, charge hatched', async () => {
    const { instance } = await mounted({
      unit: 'kWh',
      series: [
        { name: 'Discharged', points: [[1759343400000, 0.5]], type: 'bar', direction: 'discharge' },
        { name: 'Charged', points: [[1759343400000, -0.25]], type: 'bar', direction: 'charge' },
        { name: 'Untyped', points: [[1759343400000, 1]], type: 'bar' },
      ],
    })
    await waitFor(() => expect(instance.getOption().series).toHaveLength(3))
    const option = instance.getOption() as {
      series: { name: string; type: string; stack?: string; itemStyle: { color: string; borderColor: string; borderWidth: number; decal?: Record<string, unknown> } }[]
    }
    const [dis, chg, dflt] = option.series
    expect([dis.type, chg.type, dflt.type]).toEqual(['bar', 'bar', 'bar'])
    expect([dis.stack, chg.stack, dflt.stack]).toEqual(['energy', 'energy', 'energy'])
    expect(dis.itemStyle).toMatchObject({ color: 'rgba(251,146,60,0.45)', borderColor: '#fb923c', borderWidth: 1 })
    expect(dis.itemStyle.decal).toBeUndefined()
    expect(chg.itemStyle).toMatchObject({ color: 'rgba(196,181,253,0.40)', borderColor: '#c4b5fd', borderWidth: 1 })
    expect(chg.itemStyle.decal).toMatchObject({ symbol: 'rect', rotation: Math.PI / 4, color: '#c4b5fd', dashArrayX: [1, 0], dashArrayY: [2, 3] })
    expect(dflt.itemStyle).toMatchObject({ color: 'rgba(251,146,60,0.45)', borderColor: '#fb923c' })
    expect(dflt.itemStyle.decal).toBeUndefined()
  })

  it('draws an untyped series as a line with the full line option', async () => {
    const pts = [[1759343400000, 0.5]] as [number, number][]
    const { instance } = await mounted({ unit: 'kWh', series: [{ name: 'plain', points: pts }] })
    await waitFor(() => expect(instance.getOption().series).toHaveLength(1))
    const s = (instance.getOption() as { series: Record<string, unknown>[] }).series[0]
    expect(s).toMatchObject({ name: 'plain', type: 'line', showSymbol: false, data: pts })
    expect(s.stack).toBeUndefined()
    expect(s.itemStyle).toBeUndefined()
  })

  it('includes zero on the value axis when a bar series is present, and scales to the data for lines', async () => {
    const extent = (inst: echarts.ECharts) =>
      (inst as unknown as { getModel(): { getComponent(t: string): { axis: { scale: { getExtent(): number[] } } } } })
        .getModel()
        .getComponent('yAxis').axis.scale.getExtent()
    const pts = [[1759343400000, 3], [1759343460000, 4]] as [number, number][]

    const bars = await mounted({ series: [{ name: 'Discharged', points: pts, type: 'bar', direction: 'discharge' }] })
    await waitFor(() => expect(bars.instance.getOption().series).toHaveLength(1))
    expect(Math.min(...extent(bars.instance))).toBeLessThanOrEqual(0)
    expect((bars.instance.getOption() as { yAxis: { scale: boolean }[] }).yAxis[0].scale).toBe(false)
    bars.unmount()

    const lines = await mounted({ series: [{ name: 'line', points: pts }] })
    await waitFor(() => expect(lines.instance.getOption().series).toHaveLength(1))
    expect(Math.min(...extent(lines.instance))).toBeGreaterThan(0)
    expect((lines.instance.getOption() as { yAxis: { scale: boolean }[] }).yAxis[0].scale).toBe(true)
  })

  it('draws the tooltip through rich text, not HTML', async () => {
    const { instance } = await mounted()
    await waitFor(() => expect(instance.getOption().series).toHaveLength(body.series.length))
    expect((instance.getOption() as { tooltip: { renderMode: string }[] }).tooltip[0].renderMode).toBe('richText')
  })

  it('shows the series named with markup as text: showing its tooltip builds no element', async () => {
    expect(body.series.map((s) => s.name)).toContain(hostile)
    // jsdom sizes every element 0x0, and a 0x0 chart never shows a tooltip,
    // so an HTML tooltip would build nothing to find.
    const size = (name: string, px: number) => vi.spyOn(HTMLElement.prototype, name as 'clientWidth', 'get').mockReturnValue(px)
    size('clientWidth', 400)
    size('clientHeight', 300)
    const { el, instance } = await mounted()
    await waitFor(() => expect(instance.getOption().series).toHaveLength(body.series.length))
    const option = instance.getOption() as { series: { name: string }[] }
    expect(option.series.map((s) => s.name)).toContain(hostile)

    instance.dispatchAction({ type: 'showTip', seriesIndex: option.series.findIndex((s) => s.name === hostile), dataIndex: 0, x: 10, y: 10 })
    await new Promise((r) => setTimeout(r, 50))
    expect(document.body.querySelector('img')).toBeNull()
    expect(el.querySelector('img')).toBeNull()
    expect(document.body.innerHTML).not.toContain('<img')
  })

  it('hands the tooltip the hostile names with no rich-text token or line break of their own', async () => {
    const names = ['x}{y|z', 'a}\nFAKE 99{b|', 'bat-1']
    const { instance } = await mounted({
      series: names.map((name) => ({ name, points: [[1759343400000, 65.5]] as [number, number][] })),
    })
    await waitFor(() => expect(instance.getOption().series).toHaveLength(3))
    const option = instance.getOption() as {
      tooltip: { formatter: (p: unknown) => string }[]
      series: { name: string }[]
    }
    expect(option.series.map((s) => s.name)).toEqual(names)
    const params = names.map((seriesName) => ({
      seriesName,
      marker: '',
      value: [1759343400000, 65.5],
      axisValueLabel: 'T',
    }))
    const lines = option.tooltip[0].formatter(params).split('\n')
    expect(lines).toHaveLength(4)
    expect(lines[0]).toBe('T')
    for (const line of lines.slice(1, 3)) expect(line).not.toMatch(/[{}|]/)
    expect(lines[3]).toBe('bat-1: 65.5 ' + body.unit)

    const marked = option.tooltip[0].formatter([{ ...params[2], marker: '{m|}' }])
    expect(marked.match(/\{[^}]*\}/g)).toEqual(['{m|}'])
  })

  it('clears the init error once a later init succeeds', async () => {
    vi.mocked(echarts.init).mockImplementationOnce(() => {
      throw new Error('no canvas')
    })
    const view = render(DescriptorChart, { props: { body, empty: 'none' } })
    expect(await screen.findByTestId('descriptor-chart-error')).toHaveTextContent('no canvas')

    await view.rerender({ body: { unit: '%', series: [] }, empty: 'none' })
    await view.rerender({ body, empty: 'none' })
    const el = (await screen.findByTestId('descriptor-chart')) as HTMLElement
    await waitFor(() => expect(echarts.getInstanceByDom(el)).toBeDefined())
    expect(screen.queryByTestId('descriptor-chart-error')).toBeNull()
  })

  it('disposes the chart on unmount', async () => {
    const { el, instance, unmount } = await mounted()
    expect(instance.isDisposed()).toBeFalsy()
    unmount()
    expect(instance.isDisposed()).toBe(true)
    expect(echarts.getInstanceByDom(el)).toBeUndefined()
  })

  it('resizes with its container and stops observing on unmount', async () => {
    let callback: () => void = () => {}
    const observe = vi.fn()
    const disconnect = vi.fn()
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(cb: () => void) {
          callback = cb
        }
        observe = observe
        disconnect = disconnect
      },
    )
    const { el, instance, unmount } = await mounted()
    expect(observe).toHaveBeenCalledWith(el)
    const resize = vi.spyOn(instance, 'resize')
    callback()
    expect(resize).toHaveBeenCalledTimes(1)
    unmount()
    expect(disconnect).toHaveBeenCalledTimes(1)
  })

  it('says so in words when no series has a point, and builds no chart', () => {
    render(DescriptorChart, { props: { body: { unit: '%', series: [{ name: 'a', points: [] }] }, empty: 'No samples yet.' } })
    expect(screen.getByTestId('descriptor-chart-empty')).toHaveTextContent('No samples yet.')
    expect(screen.queryByTestId('descriptor-chart')).toBeNull()
  })
})

describe('DescriptorPanel with a chart section', () => {
  it('renders the chart and the tables of the fixture, leaving the table rendering as it was', async () => {
    render(DescriptorPanel, { props: { descriptor: fixture } })
    expect(await screen.findByTestId('descriptor-chart')).toBeInTheDocument()
    expect(screen.getAllByTestId('descriptor-section')).toHaveLength(4)
    expect(screen.getAllByRole('table')).toHaveLength(2)
    expect(screen.getAllByTestId('descriptor-heading').map((h) => h.textContent)).toEqual(['Registry', 'Clients', 'State of charge'])
    expect(screen.queryByTestId('descriptor-unknown-kind')).toBeNull()
  })

  it('shows a message, not a chart, for a section the second-defence check refuses', () => {
    const tooMany = { unit: 'u', series: Array.from({ length: 17 }, (_, k) => ({ name: `s${k}`, points: [[1, 1]] })) }
    render(DescriptorPanel, {
      props: { descriptor: { version: 2, sections: [{ kind: 'chart', heading: '', prose: [], empty: '', body: tooMany }] } },
    })
    expect(screen.getByTestId('descriptor-malformed-body')).toBeInTheDocument()
    expect(screen.queryByTestId('descriptor-chart')).toBeNull()
  })
})
