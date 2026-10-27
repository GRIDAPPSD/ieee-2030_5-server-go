// The badge text and fill pairs must hold WCAG AA (4.5:1) in both themes.
// The pairs are read from DescriptorCell.svelte's own rules, not restated.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { block, contrast, resolve } from '../test-tokens'

const source = readFileSync('src/panels/DescriptorCell.svelte', 'utf8')

function pair(cls: string): { bg: string; fg: string } {
  const m = source.match(new RegExp(`\\.${cls}\\s*\\{\\s*background:\\s*var\\((--[a-z-]+)\\);\\s*color:\\s*var\\((--[a-z-]+)\\);`))
  if (!m) throw new Error(`no ${cls} rule with token background and color`)
  return { bg: m[1], fg: m[2] }
}

const dark = block(':root')
const themes = { dark, light: { ...dark, ...block(":root[data-theme='light']") } }

describe('DescriptorCell badges', () => {
  for (const [theme, vars] of Object.entries(themes)) {
    for (const cls of ['badge-info', 'badge-ok', 'badge-warn', 'badge-error']) {
      it(`${cls} text holds 4.5:1 in the ${theme} theme`, () => {
        const { bg, fg } = pair(cls)
        const ratio = contrast(resolve(vars, fg), resolve(vars, bg), resolve(vars, '--surface-card'))
        expect(ratio).toBeGreaterThanOrEqual(4.5)
      })
    }
  }

  it('the contrast check can fail: the old info pair measures under 4.5', () => {
    expect(contrast('#3b82f6', '#273449', '#1e293b')).toBeLessThan(4.5)
  })
})
