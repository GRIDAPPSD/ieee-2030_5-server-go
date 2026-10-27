// Runs the cases pkg/sep2admin runs through its encoder, so the renderer's
// second defence refuses exactly what the encoder does.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { seriesOption, descriptorChartRefusals, MAX_CHART_SECTIONS, MAX_CHART_SERIES, MAX_CHART_SERIES_POINTS, MAX_DESCRIPTOR_CHART_POINTS } from './chart'
import type { ChartTheme } from './chartTheme'
import type { Descriptor, DescriptorChartBody, DescriptorSection } from './descriptor'

type WireValue = number | 'NaN' | 'Infinity' | '-Infinity'
interface WireChart {
  unit: string
  series: { name: string; points: [number, WireValue][] }[]
}
interface ChartCase {
  name: string
  refusal: string
  rendererOnly?: boolean
  generate?: number[][]
  charts?: WireChart[]
}

const doc = JSON.parse(readFileSync('../../../../pkg/sep2admin/testdata/chart_cases.json', 'utf8')) as {
  bounds: { seriesPerSection: number; pointsPerSeries: number; pointsPerDescriptor: number; chartSectionsPerDescriptor: number }
  cases: ChartCase[]
}

function value(v: WireValue): number {
  return typeof v === 'number' ? v : Number(v)
}

function chartSection(body: DescriptorChartBody): DescriptorSection {
  return { kind: 'chart', heading: '', prose: [], empty: '', body }
}

function descriptorOf(c: ChartCase): Descriptor {
  const sections: DescriptorSection[] = []
  for (const lengths of c.generate ?? []) {
    sections.push(
      chartSection({
        unit: 'u',
        series: lengths.map((n, k) => ({
          name: `s${k}`,
          points: Array.from({ length: n }, (_, j) => [1759343400000 + 60000 * j, j] as [number, number]),
        })),
      }),
    )
  }
  for (const w of c.charts ?? []) {
    sections.push(
      chartSection({
        unit: w.unit,
        series: w.series.map((s) => ({ name: s.name, points: s.points.map(([ms, v]) => [ms, value(v)] as [number, number]) })),
      }),
    )
  }
  return { version: 2, sections }
}

describe('chart refusals', () => {
  it('uses the bounds the shared fixture names', () => {
    expect([doc.bounds.seriesPerSection, doc.bounds.pointsPerSeries, doc.bounds.pointsPerDescriptor]).toEqual([
      MAX_CHART_SERIES,
      MAX_CHART_SERIES_POINTS,
      MAX_DESCRIPTOR_CHART_POINTS,
    ])
    expect(doc.bounds.chartSectionsPerDescriptor).toBe(MAX_CHART_SECTIONS)
  })

  it('refuses and accepts exactly the shared cases the Go encoder does', () => {
    expect(doc.cases.length).toBeGreaterThan(10)
    expect(doc.cases.some((c) => c.refusal === '')).toBe(true)
    expect(new Set(doc.cases.map((c) => c.refusal)).size).toBeGreaterThan(5)
    for (const code of ['too-many-sections', 'time-not-integer']) {
      expect(doc.cases.some((c) => c.refusal === code)).toBe(true)
    }
    expect(doc.cases.some((c) => c.rendererOnly)).toBe(true)
    for (const c of doc.cases) {
      const found = descriptorChartRefusals(descriptorOf(c)).find((r) => r !== null) ?? ''
      expect([c.name, found]).toEqual([c.name, c.refusal])
    }
  })

  it('refuses a body of the wrong shape as malformed', () => {
    const d: Descriptor = { version: 2, sections: [{ kind: 'chart', heading: '', prose: [], empty: '', body: { unit: 1 } }] }
    expect(descriptorChartRefusals(d)).toEqual(['malformed'])
  })
})

describe('seriesOption bars', () => {
  const theme: ChartTheme = {
    palette: ['#112233', '#445566', '#778899', '#aabbcc', '#ddeeff'],
    area: '',
    text: '',
    axis: '',
    grid: '',
    tooltipBg: '',
    tooltipBorder: '',
    direction: {
      discharge: { edge: '#fb923c', fill: 'rgba(251,146,60,0.45)' },
      charge: { edge: '#c4b5fd', fill: 'rgba(196,181,253,0.40)' },
    },
    plot: { left: 0, right: 0, top: 0, bottom: 0 },
  }
  type Item = { color: string; borderColor: string; decal?: { color: string } }
  const item = (o: Record<string, unknown>) => o.itemStyle as Item
  const bar = (direction: 'discharge' | 'charge', colorIndex?: number) =>
    seriesOption({ name: 'n', points: [], type: 'bar', direction, ...(colorIndex === undefined ? {} : { colorIndex }) }, theme)

  it('colors a palette bar by its slot, with a lighter hatched fill for charge only', () => {
    const discharge = item(bar('discharge', 1))
    const charge = item(bar('charge', 1))
    expect(discharge.color).toBe('#445566')
    expect(discharge.decal).toBeUndefined()
    expect(charge.borderColor).toBe('#445566')
    expect(charge.color).toBe('rgba(68, 85, 102, 0.35)')
    expect(charge.color).not.toBe(discharge.color)
    expect(charge.decal?.color).toBe('#445566')
  })

  it('colors a direction bar by its direction, hatching charge only', () => {
    expect(item(bar('discharge')).color).toBe('rgba(251,146,60,0.45)')
    expect(item(bar('discharge')).decal).toBeUndefined()
    expect(item(bar('charge')).color).toBe('rgba(196,181,253,0.40)')
    expect(item(bar('charge')).decal?.color).toBe('#c4b5fd')
  })

  it('wraps a palette slot past the end and passes the series id through', () => {
    expect(item(bar('discharge', 6)).color).toBe('#445566')
    expect(seriesOption({ name: 'n', points: [], type: 'bar', id: 'k:charge' }, theme).id).toBe('k:charge')
    expect(bar('discharge')).not.toHaveProperty('id')
  })
})
