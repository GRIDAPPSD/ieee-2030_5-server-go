// Layout of the descriptor chart and table at fleet size (49 series, 49 rows).
// jsdom does no layout, so the stylesheet is injected and read back through
// getComputedStyle: what is asserted is the rule the browser would apply.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { render, screen, waitFor } from '@testing-library/svelte'
import * as echarts from 'echarts/core'
import DescriptorChart from './DescriptorChart.svelte'
import DescriptorTable from './DescriptorTable.svelte'
import ActivityChart from './ActivityChart.svelte'
import { installCanvasStub } from '../test-canvas-stub'
import type { DescriptorChartBody, DescriptorTableBody } from '../lib/descriptor'

installCanvasStub()

let style: HTMLStyleElement
beforeEach(() => {
  style = document.createElement('style')
  style.textContent = readFileSync('src/app.css', 'utf8')
  document.head.appendChild(style)
})
afterEach(() => style.remove())

const fleet: DescriptorChartBody = {
  unit: '%',
  series: Array.from({ length: 49 }, (_, k) => ({
    name: `bat-${k}`,
    points: [
      [1759343400000, 60 + k / 10],
      [1759343460000, 61 + k / 10],
    ] as [number, number][],
  })),
}

async function chartOption(body: DescriptorChartBody) {
  render(DescriptorChart, { props: { body, empty: 'none' } })
  const el = (await screen.findByTestId('descriptor-chart')) as HTMLElement
  await waitFor(() => expect(echarts.getInstanceByDom(el)?.getOption().series).toHaveLength(body.series.length))
  return { el, option: echarts.getInstanceByDom(el)!.getOption() as Record<string, any[]> }
}

describe('descriptor chart at 49 series', () => {
  it('is tall enough to read', async () => {
    const { el } = await chartOption(fleet)
    expect(parseInt(getComputedStyle(el).height, 10)).toBeGreaterThanOrEqual(360)
  })

  it('has a scrolling legend with room above the plot', async () => {
    const { option } = await chartOption(fleet)
    expect(option.legend[0].type).toBe('scroll')
    expect(option.grid[0].top).toBeGreaterThanOrEqual(48)
  })

  it('labels the time axis as HH:MM:SS with overlapping labels hidden', async () => {
    const { option } = await chartOption(fleet)
    const label = option.xAxis[0].axisLabel
    expect(label.formatter).toBe('{HH}:{mm}:{ss}')
    expect(label.hideOverlap).toBe(true)
    expect(option.xAxis[0].splitNumber).toBeLessThanOrEqual(6)
  })

  it('fits the value axis to the data instead of forcing it to zero', async () => {
    const { option } = await chartOption(fleet)
    expect(option.yAxis[0].scale).toBe(true)
  })

  it('leaves the Overview chart at its old height', async () => {
    render(ActivityChart, { props: { history: [] } })
    const el = document.getElementById('activityChart') as HTMLElement
    expect(getComputedStyle(el).height).toBe('200px')
  })
})

describe('descriptor table at 49 rows', () => {
  const body: DescriptorTableBody = {
    columns: ['Name', 'State'],
    rows: Array.from({ length: 49 }, (_, k) => [
      { kind: 'text', text: `bat-${k}` },
      { kind: 'text', text: 'on' },
    ]) as DescriptorTableBody['rows'],
  }

  it('scrolls inside a bounded area', () => {
    const { container } = render(DescriptorTable, { props: { body } })
    const scroll = container.querySelector('.table-scroll') as HTMLElement
    const css = getComputedStyle(scroll)
    expect(css.maxHeight).toBe('480px')
    expect(css.overflowY).toBe('auto')
    expect(css.overflowX).toBe('auto')
    expect(screen.getAllByTestId('descriptor-cell')).toHaveLength(98)
  })

  it('keeps the header visible while the rows scroll', () => {
    render(DescriptorTable, { props: { body } })
    for (const th of screen.getAllByTestId('descriptor-column-header')) {
      expect(getComputedStyle(th).position).toBe('sticky')
      expect(getComputedStyle(th).top).toBe('0px')
    }
  })
})
