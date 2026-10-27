// Reads the design tokens out of app.css text, for tests. jsdom does not
// resolve var() chains in custom properties, so the stylesheet is parsed and
// the chains followed here; the result is the declared value the browser
// would compute on :root.
/// <reference types="node" />
import { readFileSync } from 'node:fs'

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
