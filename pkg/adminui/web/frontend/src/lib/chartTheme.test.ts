import { describe, expect, it } from 'vitest'
import { chartTheme } from './chartTheme'
import { css, darkToken, useAppCss } from '../test-tokens'

describe('chartTheme', () => {
  it('throws, naming the token, when a token is not set', () => {
    expect(() => chartTheme()).toThrow(/--chart-1 is not set/)
  })

  describe('with the stylesheet', () => {
    useAppCss()

    it('reads the palette, chrome and direction colors from the tokens', () => {
      const t = chartTheme()
      expect(t.palette).toEqual([1, 2, 3, 4, 5].map((n) => darkToken(`--chart-${n}`)))
      expect(t.axis).toBe(darkToken('--chart-axis'))
      expect(t.direction.charge.edge).toBe(darkToken('--power-charge'))
      expect(t.direction.discharge.fill).toBe(darkToken('--power-discharge-fill'))
    })

  })

  describe('plot margins', () => {
    useAppCss()

    it('reads the grid margins from the --plot tokens', () => {
      expect(chartTheme().plot).toEqual({ left: 60, right: 30, top: 56, bottom: 30 })
    })

    // The timeline and the look-ahead overlay line up with the chart only
    // while they take the same margins, so no rule may write them as a number.
    it('has every stylesheet rule that positions by them read the tokens', () => {
      const rules = ['.ev-lane-label', '.ev-track', '.ev-axis', '.ev-empty', '.chart-ahead']
      for (const sel of rules) {
        const body = css.match(new RegExp(`\\n${sel.replace('.', '\\.')} \\{([^}]*)\\}`))?.[1]
        expect(body, sel).toBeDefined()
        expect(body, sel).toMatch(/var\(--plot-(left|right)\)/)
        expect(body, sel).not.toMatch(/\b(60|30|56)px/)
      }
      expect(css.match(/\.chart-ahead \{[^}]*\}/)![0]).toMatch(/top: var\(--plot-top\);\s*bottom: var\(--plot-bottom\)/)
    })
  })

  it('throws when one token is missing from the stylesheet', () => {
    const style = document.createElement('style')
    style.textContent = css.replace('--chart-grid: var(--border);', '')
    document.head.appendChild(style)
    try {
      expect(() => chartTheme()).toThrow(/--chart-grid is not set/)
    } finally {
      style.remove()
    }
  })
})
