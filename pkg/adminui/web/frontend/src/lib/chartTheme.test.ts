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
