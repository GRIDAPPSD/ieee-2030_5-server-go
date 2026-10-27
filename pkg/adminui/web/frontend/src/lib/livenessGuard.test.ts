// A block or control is live when canCancel says so, and nowhere else. These
// checks fail when a non-test source file starts comparing a status to a
// control status word again, which is how a second liveness rule would arrive.
/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// vitest runs with the frontend directory as cwd.
const srcDir = 'src'

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = `${dir}/${e.name}`
    if (e.isDirectory()) out.push(...sourceFiles(p))
    else if (/\.(ts|svelte)$/.test(e.name) && !/\.test\.ts$/.test(e.name) && !/\.fixture\./.test(e.name)) out.push(p)
  }
  return out.sort()
}

const WORD = '(?:scheduled|active|cancelled|completed|superseded)'

// The shapes a second liveness rule takes. Each is counted per file, so a new
// file, or one more of a shape in a listed file, fails.
export const SHAPES: Record<string, RegExp> = {
  // status === 'active', 'active' !== status
  compare: new RegExp(`(?:[!=]==?\\s*['"]${WORD}['"])|(?:['"]${WORD}['"]\\s*[!=]==?)`, 'g'),
  // a list literal holding a status word, as in ['scheduled', 'active'].includes(s)
  list: new RegExp(`\\[[^\\]\\n]*['"]${WORD}['"][^\\]\\n]*\\]`, 'g'),
  // a list tested against a status, whatever it is called: LIVE.includes(x.status)
  includes: /\.includes\([^)\n]*[sS]tatus/g,
  // switch (s) { case 'active': ... }
  switch: new RegExp(`\\bcase\\s+['"]${WORD}['"]`, 'g'),
}

// The matches that exist, each with the reason it is not a liveness rule of
// its own.
const ALLOWED: Record<string, Record<string, { count: number; why: string }>> = {
  compare: {
    'src/lib/dercontrol.ts': { count: 2, why: 'canCancel itself' },
    'src/panels/DerControl.svelte': { count: 2, why: 'the form mode, and the "(ended)" label beside a status' },
    'src/panels/DispatchPane.svelte': { count: 1, why: 'delivery wording for a control already known active' },
  },
  list: { 'src/lib/controlEvents.ts': { count: 1, why: 'the statuses the wire may carry, checked when a reply is read' } },
  includes: { 'src/lib/controlEvents.ts': { count: 1, why: 'the check of a wire status against that list' } },
  switch: {},
}

const files = sourceFiles(srcDir)

describe('liveness has one definition', () => {
  it('scans the source tree', () => {
    expect(files.length).toBeGreaterThan(50)
    expect(files).toContain('src/lib/dercontrol.ts')
    expect(files).toContain('src/lib/gridblocks.ts')
    expect(files).toContain('src/panels/GridBlocksPane.svelte')
  })

  for (const [shape, re] of Object.entries(SHAPES)) {
    it(`no non-test file uses the ${shape} shape on a control status, except the allowed ones and only that many times`, () => {
      const found: Record<string, number> = {}
      for (const f of files) {
        const n = (readFileSync(f, 'utf8').match(re) ?? []).length
        if (n > 0) found[f] = n
      }
      const allowed = Object.fromEntries(Object.entries(ALLOWED[shape]).map(([f, a]) => [f, a.count]))
      expect(found).toEqual(allowed)
    })
  }

  // Each shape must be shown to fire on a second rule written that way, and
  // to stay quiet on code that is not one.
  it.each([
    ['compare', "if (s.status === 'cancelled') return false", 1],
    ['compare', "return 'active' !== status", 1],
    ['list', "const live = ['scheduled', 'active']", 1],
    ['list', "return ['scheduled', 'active'].includes(s)", 1],
    ['includes', 'return LIVE.includes(row.status)', 1],
    ['includes', 'LIVE.includes(  block.eventStatus.status )', 1],
    ['switch', "switch (s) {\n  case 'active':\n    return true", 1],
  ])('the %s pattern fires on a second rule: %s', (shape, code, n) => {
    expect(code.match(SHAPES[shape])).toHaveLength(n)
  })

  it.each([
    ['compare', "const m: 'scheduled' | 'default' = 'scheduled'"],
    ['list', "const a = ['discharge', 'charge']"],
    ['includes', 'return KNOWN.includes(row.kind)'],
    ['switch', "switch (k) { case 'default': return 1 }"],
  ])('the %s pattern stays quiet on unrelated code: %s', (shape, code) => {
    expect(code.match(SHAPES[shape])).toBeNull()
  })

  it('both block views take their blocks from canCancel through isLiveBlock', () => {
    const lib = readFileSync('src/lib/gridblocks.ts', 'utf8')
    expect(lib).toMatch(/import \{ canCancel \} from '\.\/dercontrol'/)
    expect(lib).toMatch(/s\.status, e\.block\.start, e\.block\.durationSeconds, serverNowSeconds/)
    const pane = readFileSync('src/panels/GridBlocksPane.svelte', 'utf8')
    expect(pane).toContain('isLiveBlock(e, serverNow)')
    expect(pane).toContain('shownBlocks(entries, liveOnly, serverNow)')
  })
})
