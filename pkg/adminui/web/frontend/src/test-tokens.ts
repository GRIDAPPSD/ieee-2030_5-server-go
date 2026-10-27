// Reads the design tokens out of app.css text, for tests. jsdom does not
// resolve var() chains in custom properties, so the stylesheet is parsed and
// the chains followed here; the result is the declared value the browser
// would compute on :root.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach } from 'vitest'

export const css = readFileSync('src/app.css', 'utf8')

export function block(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`)
  if (start < 0) throw new Error(`no ${selector} block in app.css`)
  const body = css.slice(css.indexOf('{', start) + 1, css.indexOf('\n}', start))
  const out: Record<string, string> = {}
  for (const m of body.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim()
  return out
}

export function resolve(vars: Record<string, string>, name: string, depth = 0): string {
  const v = vars[name]
  if (v === undefined) throw new Error(`${name} is not declared`)
  if (depth > 8) throw new Error(`${name} loops`)
  const ref = v.match(/^var\((--[a-z0-9-]+)\)$/)
  return ref ? resolve(vars, ref[1], depth + 1) : v
}

// The dark value of a token, the theme a test sees with no data-theme set,
// with whitespace removed because jsdom serializes rgba() without spaces.
export function darkToken(name: string): string {
  return resolve(block(':root'), name).replace(/\s+/g, '')
}

// Injects app.css into the document around each test, so getComputedStyle
// sees the tokens and rules the browser would. Call it inside a describe, or
// at the top of a test file.
export function useAppCss(): void {
  let style: HTMLStyleElement
  beforeEach(() => {
    style = document.createElement('style')
    style.textContent = css
    document.head.appendChild(style)
  })
  afterEach(() => style.remove())
}

function channels(color: string): [number, number, number, number] {
  const hex = color.match(/^#([0-9a-f]{6})$/i)
  if (hex) return [0, 2, 4].map((i) => parseInt(hex[1].slice(i, i + 2), 16)).concat(1) as [number, number, number, number]
  const rgba = color.match(/^rgba?\(([^)]*)\)$/)
  if (!rgba) throw new Error(`cannot read color ${color}`)
  const [r, g, b, a] = rgba[1].split(',').map((n) => parseFloat(n))
  return [r, g, b, a ?? 1]
}

// WCAG 2.x contrast of text over a background, the background blended over
// the surface it sits on when it is translucent.
export function contrast(text: string, bg: string, surface: string): number {
  const base = channels(surface)
  const [r, g, b, a] = channels(bg)
  const solid = [r, g, b].map((c, i) => c * a + base[i] * (1 - a))
  const lum = (rgb: number[]) => {
    const [lr, lg, lb] = rgb.map((c) => {
      const v = c / 255
      return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
    })
    return 0.2126 * lr + 0.7152 * lg + 0.0722 * lb
  }
  const l1 = lum(channels(text).slice(0, 3))
  const l2 = lum(solid)
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)
}
