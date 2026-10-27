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
export const STATUS_COMPARE = new RegExp(`(?:[!=]==?\\s*['"]${WORD}['"])|(?:['"]${WORD}['"]\\s*[!=]==?)`, 'g')

// The comparisons that exist, each with the reason it is not a liveness rule
// of its own. A new file, or a second comparison in a listed one, fails.
const ALLOWED: Record<string, { count: number; why: string }> = {
  'src/lib/dercontrol.ts': { count: 2, why: 'canCancel itself' },
  'src/panels/DerControl.svelte': { count: 2, why: 'the form mode, and the "(ended)" label beside a status' },
  'src/panels/DispatchPane.svelte': { count: 1, why: 'delivery wording for a control already known active' },
}

const files = sourceFiles(srcDir)

describe('liveness has one definition', () => {
  it('scans the source tree', () => {
    expect(files.length).toBeGreaterThan(50)
    expect(files).toContain('src/lib/dercontrol.ts')
    expect(files).toContain('src/lib/gridblocks.ts')
    expect(files).toContain('src/panels/GridBlocksPane.svelte')
  })

  it('no non-test file compares a status to a control status word, except the allowed ones and only that many times', () => {
    const found: Record<string, number> = {}
    for (const f of files) {
      const n = (readFileSync(f, 'utf8').match(STATUS_COMPARE) ?? []).length
      if (n > 0) found[f] = n
    }
    expect(found).toEqual(Object.fromEntries(Object.entries(ALLOWED).map(([f, a]) => [f, a.count])))
  })

  it('the pattern can fail: it matches a second rule written either way round', () => {
    expect("if (s.status === 'cancelled') return false".match(STATUS_COMPARE)).toHaveLength(1)
    expect("return 'active' !== status".match(STATUS_COMPARE)).toHaveLength(1)
    expect("const m: 'scheduled' | 'default' = 'scheduled'".match(STATUS_COMPARE)).toBeNull()
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
