// The design tokens must leave every pre-token variable at the value it had.
// jsdom does not resolve var() chains in custom properties, so the stylesheet
// is parsed and the chains resolved here; what is asserted is the declared value
// the browser would compute on :root.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { DIRECTION_COLORS } from './lib/chart'

const css = readFileSync('src/app.css', 'utf8')

function block(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`)
  if (start < 0) throw new Error(`no ${selector} block in app.css`)
  const body = css.slice(css.indexOf('{', start) + 1, css.indexOf('\n}', start))
  const out: Record<string, string> = {}
  for (const m of body.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim()
  return out
}

function resolve(vars: Record<string, string>, name: string, depth = 0): string {
  const v = vars[name]
  if (v === undefined) throw new Error(`${name} is not declared`)
  if (depth > 8) throw new Error(`${name} loops`)
  const ref = v.match(/^var\((--[a-z0-9-]+)\)$/)
  return ref ? resolve(vars, ref[1], depth + 1) : v
}

const squash = (v: string) => v.replace(/\s+/g, '')

// The values app.css declared before the tokens landed.
const BEFORE: Record<string, string> = {
  '--bg': '#0f172a',
  '--card': '#1e293b',
  '--border': '#334155',
  '--text': '#e2e8f0',
  '--text-h': '#e2e8f0',
  '--dim': '#94a3b8',
  '--accent': '#3b82f6',
  '--green': '#22c55e',
  '--red': '#ef4444',
}

describe('app.css design tokens', () => {
  const dark = block(':root')

  it.each(Object.entries(BEFORE))('keeps %s at its old value', (name, want) => {
    expect(resolve(dark, name)).toBe(want)
  })

  it('points each legacy variable at a token', () => {
    expect(dark['--bg']).toBe('var(--surface-page)')
    expect(dark['--card']).toBe('var(--surface-card)')
    expect(dark['--dim']).toBe('var(--text-muted)')
  })

  it('declares the power direction tokens equal to the chart literals', () => {
    expect(squash(resolve(dark, '--power-discharge'))).toBe(DIRECTION_COLORS.discharge.edge)
    expect(squash(resolve(dark, '--power-discharge-fill'))).toBe(DIRECTION_COLORS.discharge.fill)
    expect(squash(resolve(dark, '--power-charge'))).toBe(DIRECTION_COLORS.charge.edge)
    expect(squash(resolve(dark, '--power-charge-fill'))).toBe(DIRECTION_COLORS.charge.fill)
  })

  it('gives the light theme a value for every themed token and keeps the legacy names unset', () => {
    const light = block(":root[data-theme='light']")
    expect(light['--surface-page']).toBe('#f8fafc')
    expect(light['--text']).toBe('#0f172a')
    expect(light['--power-discharge']).toBe('#9a3412')
    for (const legacy of Object.keys(BEFORE).filter((n) => !['--text', '--border', '--accent'].includes(n))) {
      expect(light[legacy]).toBeUndefined()
    }
    for (const name of Object.keys(light)) expect(dark[name], `${name} has no dark value`).toBeDefined()
  })

  it('declares the type and spacing scales', () => {
    expect(resolve(dark, '--fs-3')).toBe('13px')
    expect(resolve(dark, '--sp-6')).toBe('24px')
    expect(resolve(dark, '--radius-md')).toBe('8px')
  })
})
