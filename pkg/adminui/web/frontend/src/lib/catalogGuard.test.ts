// The control form is built from the catalog's value kinds, so a new catalog
// row needs no frontend change. These checks fail when a non-test source
// file starts naming a control function again or reads a row's prose value.
/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs'
import ts from 'typescript'
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

// The files below are bound to one element by design and are not part of the
// control form: the Dispatch tab and the grid blocks send target power only.
// An entry names the elements a file may name, so a second one in the same file
// fails; each must still be named, so the list shrinks when the names go.
const ELEMENT_ALLOWED: Record<string, { elements: string[]; why: string }> = {
  'src/lib/gridrows.ts': { elements: ['opModTargetW'], why: 'the confirm step words the wire value of the target power' },
  'src/panels/DispatchPane.svelte': { elements: ['opModTargetW'], why: 'Dispatch tab reads the target power of a control' },
}

const files = sourceFiles(srcDir)

describe('the control form names no control function', () => {
  it('scans the source tree', () => {
    expect(files.length).toBeGreaterThan(50)
    expect(files).toContain('src/panels/DerControl.svelte')
    expect(files).toContain('src/lib/controlcatalog.ts')
  })

  it('no non-test file under src names an element, except the allowed ones and only the allowed elements', () => {
    const named: Record<string, string[]> = {}
    for (const f of files) {
      const found = new Set(readFileSync(f, 'utf8').match(new RegExp(ELEMENT.source, 'g')) ?? [])
      if (found.size > 0) named[f] = [...found].sort()
    }
    const allowed = Object.fromEntries(Object.entries(ELEMENT_ALLOWED).map(([f, a]) => [f, a.elements.slice().sort()]))
    expect(named).toEqual(allowed)
  })

  it('the catalog row type has no value member and no index signature to read one through', () => {
    const src = readFileSync('src/lib/controlcatalog.ts', 'utf8')
    const row = /export interface CatalogRow \{([\s\S]*?)\n\}/.exec(src)
    expect(row).not.toBeNull()
    expect(/^\s*value\??:/m.test(row?.[1] ?? '')).toBe(false)
    expect(/\[\s*\w+\s*:\s*string\s*\]/.test(row?.[1] ?? '')).toBe(false)
  })
})

// readsValue type-checks a snippet against the real CatalogRow and returns
// its diagnostics. Any read of a row's value, by whatever name the row has
// or however it is destructured, is a compile error because the type has no
// such member; this test only type-checks these snippets, and the tree itself is
// enforced by the separate npm run check step in CI.
function diagnostics(snippet: string): string[] {
  const probe = 'src/lib/__probe.ts'
  const options: ts.CompilerOptions = {
    strict: true,
    noEmit: true,
    skipLibCheck: true,
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ESNext,
    moduleResolution: ts.ModuleResolutionKind.Bundler,
    types: [],
  }
  const host = ts.createCompilerHost(options)
  const getSourceFile = host.getSourceFile.bind(host)
  host.getSourceFile = (name, lang, ...rest) =>
    name.endsWith(probe) ? ts.createSourceFile(name, snippet, lang) : getSourceFile(name, lang, ...rest)
  const fileExists = host.fileExists.bind(host)
  host.fileExists = (name) => name.endsWith(probe) || fileExists(name)
  const program = ts.createProgram([probe], options, host)
  return ts.getPreEmitDiagnostics(program).map((d) => ts.flattenDiagnosticMessageText(d.messageText, '\n'))
}

describe('reading a catalog row\'s value text is a compile error', () => {
  const head = "import type { CatalogRow, CatalogResponse } from './controlcatalog'\ndeclare const r: CatalogRow\ndeclare const resp: CatalogResponse\n"

  it('a clean snippet has no diagnostics, so the check can pass', () => {
    expect(diagnostics(head + 'export const ok = [r.kind, r.name, resp.functions.map((x) => x.kind)]\n')).toEqual([])
  })

  it.each([
    ['property read under another name', 'export const x = r.value'],
    ['destructuring', 'export const { value } = r'],
    ['read through the response list', 'export const x = resp.functions[0]?.value'],
    ['read in a callback', 'export const x = resp.functions.map((q) => q.value)'],
  ])('%s fails to compile', (_name, code) => {
    expect(diagnostics(head + code + '\n').length).toBeGreaterThan(0)
  })
})
