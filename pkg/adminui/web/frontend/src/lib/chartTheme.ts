// ECharts draws to a canvas and cannot read a CSS variable, so the colors
// are read from the tokens in app.css when a chart builds its option.
import type { ChartDirection } from './descriptor'

export interface ChartTheme {
  // Series colors, in the order ECharts assigns them.
  palette: string[]
  area: string
  text: string
  axis: string
  grid: string
  tooltipBg: string
  tooltipBorder: string
  direction: Record<ChartDirection, { edge: string; fill: string }>
}

// A browser returns a custom property already substituted; the loop only
// matters where the engine hands back a bare var() reference.
const reported = new Set<string>()

function read(style: CSSStyleDeclaration, name: string): string {
  let value = style.getPropertyValue(name).trim()
  for (let i = 0; i < 8; i++) {
    const ref = value.match(/^var\((--[a-z0-9-]+)\)$/)
    if (ref === null) break
    value = style.getPropertyValue(ref[1]).trim()
  }
  if (value === '') {
    // A missing token must not render as an invisible series. A dev build and
    // the tests fail; a production build says so once and ECharts falls back.
    const message = `chartTheme: design token ${name} is not set`
    if (import.meta.env.DEV) throw new Error(message)
    if (!reported.has(name)) {
      reported.add(name)
      console.error(message)
    }
  }
  return value
}

export function chartTheme(root: Element = document.documentElement): ChartTheme {
  const style = getComputedStyle(root)
  const get = (name: string) => read(style, name)
  return {
    palette: [1, 2, 3, 4, 5].map((n) => get(`--chart-${n}`)),
    area: get('--chart-area'),
    text: get('--text'),
    axis: get('--chart-axis'),
    grid: get('--chart-grid'),
    tooltipBg: get('--chart-tooltip-bg'),
    tooltipBorder: get('--border'),
    direction: {
      discharge: { edge: get('--power-discharge'), fill: get('--power-discharge-fill') },
      charge: { edge: get('--power-charge'), fill: get('--power-charge-fill') },
    },
  }
}

// The tooltip box shared by both charts.
export function tooltipStyle(theme: ChartTheme) {
  return {
    backgroundColor: theme.tooltipBg,
    borderColor: theme.tooltipBorder,
    textStyle: { color: theme.text },
  }
}
