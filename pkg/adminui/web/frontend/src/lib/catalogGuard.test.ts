// The control form is built from the catalog's value kinds, so a new catalog
// row needs no frontend change. These checks fail when a non-test source
// file starts naming a control function again or reads a row's prose value.
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

// A control function is an element named opMod followed by a capital.
export const ELEMENT = /\bopMod[A-Z]\w*/

// A catalog row read for its prose `value`: a row-like identifier followed by
// .value, or a row's value used as an index.
export const ROW_VALUE = /\b(row|fn|func|fun|catalogRow|definition|spec)\??\.value\b/

// The two files below are bound to one element by design and are not part of
// the control form: the Dispatch tab sends target power only, and the FSAs
// tab edits the stored default of a template by its legacy type. Each entry
// must still name an element, so the list shrinks when the names go.
const ELEMENT_ALLOWED: Record<string, string> = {
  'src/lib/derprogram.ts': 'FSA template default control, read by legacy type',
  'src/panels/DispatchPane.svelte': 'Dispatch tab reads the target power of a control',
}

const files = sourceFiles(srcDir)

describe('the control form names no control function', () => {
  it('scans the source tree', () => {
    expect(files.length).toBeGreaterThan(50)
    expect(files).toContain('src/panels/DerControl.svelte')
    expect(files).toContain('src/lib/controlcatalog.ts')
  })

  it('no non-test file under src names an element, except the allowed two', () => {
    const named = files.filter((f) => ELEMENT.test(readFileSync(f, 'utf8')))
    expect(named).toEqual(Object.keys(ELEMENT_ALLOWED).sort())
  })

  it('no non-test file reads a catalog row\'s value text', () => {
    const reading = files.filter((f) => ROW_VALUE.test(readFileSync(f, 'utf8')))
    expect(reading).toEqual([])
  })

  it('the catalog row type has no value member to read', () => {
    const src = readFileSync('src/lib/controlcatalog.ts', 'utf8')
    const row = /export interface CatalogRow \{([\s\S]*?)\n\}/.exec(src)
    expect(row).not.toBeNull()
    expect(/^\s*value\??:/m.test(row?.[1] ?? '')).toBe(false)
  })

  it('the checks can fail: each pattern matches a line it must catch and skips one it must not', () => {
    expect(ELEMENT.test('const k = "opModMaxLimW"')).toBe(true)
    expect(ELEMENT.test('opMode is not an element')).toBe(false)
    expect(ROW_VALUE.test('const text = row.value')).toBe(true)
    expect(ROW_VALUE.test('const text = input.value')).toBe(false)
  })
})
