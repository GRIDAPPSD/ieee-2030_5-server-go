// The chart bounds and refusal rules pkg/sep2admin enforces at encode,
// applied again as a second defence. testdata/chart_cases.json holds the
// cases both sides run; the codes below are the ones it names.
import type { Descriptor, DescriptorChartBody } from './descriptor'

export const MAX_CHART_SERIES = 16
export const MAX_CHART_SERIES_POINTS = 720
export const MAX_DESCRIPTOR_CHART_POINTS = 12000

// The range a JavaScript Date holds, in milliseconds either side of the epoch.
const MAX_CHART_MILLIS = 8_640_000_000_000_000

export type ChartRefusal =
  | 'too-many-series'
  | 'series-too-long'
  | 'too-many-points'
  | 'value-not-finite'
  | 'points-out-of-order'
  | 'series-without-name'
  | 'time-out-of-range'
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
      if (!Number.isInteger(ms) || Math.abs(ms) > MAX_CHART_MILLIS) return 'time-out-of-range'
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
  return d.sections.map((section) => {
    if (section.kind !== 'chart') return null
    return isChartBody(section.body) ? chartRefusal(section.body, total) : 'malformed'
  })
}
