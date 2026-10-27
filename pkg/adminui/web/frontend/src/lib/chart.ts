// The chart bounds and refusal rules pkg/sep2admin enforces at encode,
// applied again as a second defence. testdata/chart_cases.json holds the
// cases both sides run; the codes below are the ones it names.
import type { ChartDirection, Descriptor, DescriptorChartBody, DrawnChartSeries } from './descriptor'

export const MAX_CHART_SERIES = 16
export const MAX_CHART_SERIES_POINTS = 720
export const MAX_DESCRIPTOR_CHART_POINTS = 12000
export const MAX_CHART_SECTIONS = 8

// The range a JavaScript Date holds, in milliseconds either side of the epoch.
const MAX_CHART_MILLIS = 8_640_000_000_000_000

export type ChartRefusal =
  | 'too-many-series'
  | 'series-too-long'
  | 'too-many-points'
  | 'too-many-sections'
  | 'value-not-finite'
  | 'points-out-of-order'
  | 'series-without-name'
  | 'time-out-of-range'
  | 'time-not-integer'
  | 'malformed'

export function isChartBody(value: unknown): value is DescriptorChartBody {
  if (typeof value !== 'object' || value === null) return false
  const body = value as Partial<DescriptorChartBody>
  return (
    typeof body.unit === 'string' &&
    Array.isArray(body.series) &&
    body.series.every(
      (s) =>
        typeof s === 'object' &&
        s !== null &&
        typeof s.name === 'string' &&
        Array.isArray(s.points) &&
        s.points.every((p) => Array.isArray(p) && p.length === 2 && typeof p[0] === 'number' && typeof p[1] === 'number'),
    )
  )
}

// total is the running point count across the Descriptor's chart sections.
export function chartRefusal(body: DescriptorChartBody, total: { points: number }): ChartRefusal | null {
  if (body.series.length > MAX_CHART_SERIES) return 'too-many-series'
  for (const s of body.series) {
    if (s.name === '') return 'series-without-name'
    if (s.points.length > MAX_CHART_SERIES_POINTS) return 'series-too-long'
    total.points += s.points.length
    if (total.points > MAX_DESCRIPTOR_CHART_POINTS) return 'too-many-points'
    let last = 0
    for (let j = 0; j < s.points.length; j++) {
      const [ms, value] = s.points[j]
      if (!Number.isFinite(value)) return 'value-not-finite'
      if (Math.abs(ms) > MAX_CHART_MILLIS) return 'time-out-of-range'
      if (!Number.isInteger(ms)) return 'time-not-integer'
      if (j > 0 && ms <= last) return 'points-out-of-order'
      last = ms
    }
  }
  return null
}

// One entry per section: the refusal for a chart section, null for the
// rest. A body of the wrong shape is 'malformed'.
export function descriptorChartRefusals(d: Descriptor): (ChartRefusal | null)[] {
  const total = { points: 0 }
  let charts = 0
  return d.sections.map((section) => {
    if (section.kind !== 'chart') return null
    if (++charts > MAX_CHART_SECTIONS) return 'too-many-sections'
    return isChartBody(section.body) ? chartRefusal(section.body, total) : 'malformed'
  })
}

// ECharts wraps a series name as {style|name} in rich text without escaping
// it, so a name holding braces, a bar or a line break can forge tokens or
// lines. Lookalike characters keep the name readable.
export function richTextSafe(text: string): string {
  return text.replace(/[{}|]/g, (c) => ({ '{': '\uFF5B', '}': '\uFF5D', '|': '\uFF5C' })[c] ?? c).replace(/[\r\n]+/g, ' ')
}

export interface TooltipParam {
  marker?: string
  seriesName?: string
  value?: unknown
  axisValueLabel?: string
}

// The tooltip text for one axis position. Names and the unit are wire text,
// so each goes through richTextSafe; the legend keeps the real name.
export function chartTooltip(unit: string): (params: TooltipParam | TooltipParam[]) => string {
  const suffix = unit === '' ? '' : ' ' + richTextSafe(unit)
  return (params) => {
    const rows = Array.isArray(params) ? params : [params]
    const lines = rows.map((p) => {
      const v = Array.isArray(p.value) ? p.value[1] : p.value
      return `${p.marker ?? ''}${richTextSafe(p.seriesName ?? '')}: ${String(v)}${suffix}`
    })
    return [rows[0]?.axisValueLabel ?? '', ...lines].join('\n')
  }
}

// The dark-theme direction colors from the admin UI theme design. They live
// here until the theme tokens land and replace them.
export const DIRECTION_COLORS: Record<ChartDirection, { edge: string; fill: string }> = {
  discharge: { edge: '#fb923c', fill: 'rgba(251,146,60,0.45)' },
  charge: { edge: '#c4b5fd', fill: 'rgba(196,181,253,0.40)' },
}

// Charge carries a diagonal hatch so the two directions never rest on hue.
const CHARGE_DECAL = { symbol: 'rect', dashArrayX: [1, 0], dashArrayY: [2, 3], rotation: Math.PI / 4, color: DIRECTION_COLORS.charge.edge }

// The ECharts series for one body series. Bars on one stack share a
// timestamp, so discharge above zero and charge below never sit side by side.
export function seriesOption(s: DrawnChartSeries): Record<string, unknown> {
  if (s.type !== 'bar') return { name: s.name, type: 'line', showSymbol: false, data: s.points }
  const colors = DIRECTION_COLORS[s.direction ?? 'discharge']
  return {
    name: s.name,
    type: 'bar',
    stack: 'energy',
    data: s.points,
    itemStyle: {
      color: colors.fill,
      borderColor: colors.edge,
      borderWidth: 1,
      ...(s.direction === 'charge' ? { decal: CHARGE_DECAL } : {}),
    },
  }
}
