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
import type { Descriptor, DescriptorChartBody } from '../lib/descriptor'

installCanvasStub()

const fixture = JSON.parse(
  readFileSync('../../../../pkg/sep2admin/testdata/descriptor_v2.json', 'utf8'),
) as Descriptor
const chartSection = fixture.sections.find((s) => s.kind === 'chart')!
const body = chartSection.body as DescriptorChartBody
const hostile = '<img src=x onerror=alert(1)>'

afterEach(() => vi.unstubAllGlobals())

async function mounted(over: Partial<DescriptorChartBody> = {}) {
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

  it('carries the fixture series named with markup as text: rich-text tooltip, no element built from it', async () => {
    expect(body.series.map((s) => s.name)).toContain(hostile)
    const { el, instance } = await mounted()
    await waitFor(() => expect(instance.getOption().series).toHaveLength(body.series.length))
    const option = instance.getOption() as { tooltip: { renderMode: string }[]; series: { name: string }[] }
    expect(option.tooltip[0].renderMode).toBe('richText')
    expect(option.series.map((s) => s.name)).toContain(hostile)

    const hostileIndex = option.series.findIndex((s) => s.name === hostile)
    instance.dispatchAction({ type: 'showTip', seriesIndex: hostileIndex, dataIndex: 0 })
    expect(document.body.querySelector('img')).toBeNull()
    expect(el.querySelector('img')).toBeNull()
    expect(document.body.innerHTML).not.toContain('<img')
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
