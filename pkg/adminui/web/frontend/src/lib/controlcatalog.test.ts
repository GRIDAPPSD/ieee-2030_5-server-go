import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  buildDeviceControl,
  buildFunctions,
  buildValue,
  curvesOfType,
  emptyFieldState,
  fieldStateFromFunctions,
  functionsText,
  initialValue,
  loadCatalog,
  loadCurves,
  primacyText,
  uneditableFunctions,
  type CatalogRow,
  type ControlFormInput,
  type FieldValue,
} from './controlcatalog'

// One row of each kind. The names are arbitrary on purpose: the logic is
// driven by kind, min, max, fields and curveType alone.
const flag: CatalogRow = { name: 'rowA', description: 'a flag', kind: 'boolean' }
const level: CatalogRow = { name: 'rowB', description: 'a level', kind: 'integer', min: -5, max: 100 }
const pair: CatalogRow = {
  name: 'rowC',
  description: 'an object',
  kind: 'object',
  fields: [
    { name: 'amount', kind: 'integer', required: true, min: 1, max: 1000 },
    { name: 'sign', kind: 'boolean', required: true },
    { name: 'scale', kind: 'integer', min: -9, max: 9 },
  ],
}
const curve: CatalogRow = { name: 'rowD', description: 'a curve', kind: 'curve', curveType: 7 }
const rows = [flag, level, pair, curve]

describe('initialValue', () => {
  it('starts booleans false, integers and curves empty, and an object from its fields', () => {
    expect(initialValue(flag)).toBe(false)
    expect(initialValue(level)).toBe('')
    expect(initialValue(curve)).toBe('')
    expect(initialValue(pair)).toEqual({ amount: '', sign: false, scale: '' })
  })
})

describe('buildValue per kind', () => {
  it('boolean posts true or false', () => {
    expect(buildValue(flag, true)).toEqual({ ok: true, value: true })
    expect(buildValue(flag, false)).toEqual({ ok: true, value: false })
  })

  it('integer posts a number inside min and max, bounds included', () => {
    expect(buildValue(level, '-5')).toEqual({ ok: true, value: -5 })
    expect(buildValue(level, ' 100 ')).toEqual({ ok: true, value: 100 })
  })

  it('integer refuses one past each bound, a decimal, text and empty', () => {
    for (const bad of ['-6', '101', '1.5', 'abc', '', '1e2']) {
      const r = buildValue(level, bad)
      expect(r.ok, bad).toBe(false)
    }
    const r = buildValue(level, '101')
    expect(r).toEqual({ ok: false, error: 'rowB must be a whole number from -5 to 100.' })
  })

  it('object posts members by name, leaving an empty optional member out', () => {
    expect(buildValue(pair, { amount: '950', sign: true, scale: '' })).toEqual({ ok: true, value: { amount: 950, sign: true } })
    expect(buildValue(pair, { amount: '950', sign: false, scale: '-3' })).toEqual({
      ok: true,
      value: { amount: 950, sign: false, scale: -3 },
    })
  })

  it('an optional boolean never set is omitted; once set, true or false is sent; a required one is always sent', () => {
    const row: CatalogRow = {
      name: 'rowF',
      description: '',
      kind: 'object',
      fields: [
        { name: 'amount', kind: 'integer', required: true, min: 0, max: 9 },
        { name: 'flag', kind: 'boolean' },
        { name: 'must', kind: 'boolean', required: true },
      ],
    }
    expect(initialValue(row)).toEqual({ amount: '', flag: '', must: false })
    expect(buildValue(row, { amount: '3', flag: '', must: false })).toEqual({ ok: true, value: { amount: 3, must: false } })
    expect(buildValue(row, { amount: '3', flag: true, must: true })).toEqual({ ok: true, value: { amount: 3, flag: true, must: true } })
    expect(buildValue(row, { amount: '3', flag: false, must: false })).toEqual({ ok: true, value: { amount: 3, flag: false, must: false } })
  })

  it('object refuses an empty required member and an out-of-range member, naming it', () => {
    expect(buildValue(pair, { amount: '', sign: false, scale: '' })).toEqual({ ok: false, error: 'rowC: amount is required.' })
    expect(buildValue(pair, { amount: '1001', sign: false, scale: '' })).toEqual({
      ok: false,
      error: 'rowC: amount must be a whole number from 1 to 1000.',
    })
    expect(buildValue(pair, { amount: '5', sign: false, scale: '10' }).ok).toBe(false)
  })

  it('curve posts the picked id and refuses none', () => {
    expect(buildValue(curve, 'c9')).toEqual({ ok: true, value: 'c9' })
    expect(buildValue(curve, '')).toEqual({ ok: false, error: 'rowD: pick a curve.' })
  })

  it('an unknown kind is refused, never sent', () => {
    const r = buildValue({ name: 'rowE', description: '', kind: 'matrix' }, '')
    expect(r.ok).toBe(false)
  })
})

describe('buildFunctions', () => {
  const values = (over: Record<string, FieldValue> = {}): Record<string, FieldValue> => ({
    rowA: true,
    rowB: '40',
    rowC: { amount: '950', sign: false, scale: '' },
    rowD: 'c1',
    ...over,
  })

  it('posts every ticked row and no unticked one, in catalog order', () => {
    const r = buildFunctions(rows, { rowD: true, rowA: true, rowC: false }, values())
    expect(r).toEqual({ ok: true, functions: { rowA: true, rowD: 'c1' }, names: ['rowA', 'rowD'] })
  })

  it('several functions together carry each one value', () => {
    const r = buildFunctions(rows, { rowA: true, rowB: true, rowC: true }, values())
    expect(r.ok && r.functions).toEqual({ rowA: true, rowB: 40, rowC: { amount: 950, sign: false } })
  })

  it('an empty selection is refused', () => {
    expect(buildFunctions(rows, {}, values())).toEqual({ ok: false, error: 'Tick at least one function.' })
  })

  it('a bad value in a ticked row stops the build, an unticked bad value does not', () => {
    expect(buildFunctions(rows, { rowB: true }, values({ rowB: 'x' })).ok).toBe(false)
    expect(buildFunctions(rows, { rowA: true }, values({ rowB: 'x' })).ok).toBe(true)
  })
})

describe('buildDeviceControl', () => {
  const input: ControlFormInput = {
    mode: 'scheduled',
    target: 'device 222222222222',
    functions: { rowA: true },
    startNow: true,
    startAtLocal: '',
    durationMinutes: 5,
    description: '',
  }

  it('a scheduled control posts kind, functions and the duration in seconds', () => {
    const r = buildDeviceControl(input)
    expect(r.ok && r.body).toEqual({ kind: 'scheduled', functions: { rowA: true }, durationSeconds: 300 })
    expect(r.ok && r.summary).toBe('Schedule rowA=true on device 222222222222, starting now, for 5 min.')
  })

  it('a chosen start time is sent as Unix seconds and named in the summary', () => {
    const r = buildDeviceControl({ ...input, startNow: false, startAtLocal: '2030-01-02T03:04:05Z' })
    const seconds = Math.floor(Date.parse('2030-01-02T03:04:05Z') / 1000)
    expect(r.ok && r.body.startTime).toBe(seconds)
    expect(r.ok && r.summary).toContain(new Date(seconds * 1000).toISOString())
  })

  it('a default posts kind and functions only: no time, duration or description', () => {
    const r = buildDeviceControl({ ...input, mode: 'default', durationMinutes: null, description: 'ignored' })
    expect(r.ok && r.body).toEqual({ kind: 'default', functions: { rowA: true } })
    expect(r.ok && r.summary).toContain('in force now')
    expect(r.ok && r.summary).toContain('no end')
  })

  it('refuses a missing or non-positive duration, a missing start and a long description', () => {
    expect(buildDeviceControl({ ...input, durationMinutes: null }).ok).toBe(false)
    expect(buildDeviceControl({ ...input, durationMinutes: 0 }).ok).toBe(false)
    expect(buildDeviceControl({ ...input, durationMinutes: 0.001 }).ok).toBe(false)
    expect(buildDeviceControl({ ...input, startNow: false }).ok).toBe(false)
    expect(buildDeviceControl({ ...input, description: 'x'.repeat(33) }).ok).toBe(false)
    expect(buildDeviceControl({ ...input, description: 'x'.repeat(32) }).ok).toBe(true)
  })

  it('states a duration that is not whole minutes in seconds', () => {
    const r = buildDeviceControl({ ...input, durationMinutes: 1.5 })
    expect(r.ok && r.summary).toContain('for 90 s.')
  })
})

describe('display helpers', () => {
  it('functionsText lists name=value pairs, and a dash for none', () => {
    expect(functionsText({ rowA: true, rowC: { amount: 5 } })).toBe('rowA=true, rowC={"amount":5}')
    expect(functionsText({})).toBe('-')
    expect(functionsText(undefined)).toBe('-')
  })

  it('curvesOfType keeps only the curves of the asked type', () => {
    const all = [
      { id: 'a', curveType: 7, description: '' },
      { id: 'b', curveType: 8, description: '' },
    ]
    expect(curvesOfType(all, 7).map((c) => c.id)).toEqual(['a'])
    expect(curvesOfType(all, undefined)).toEqual([])
  })

  it('primacyText states the number and the server reason', () => {
    expect(primacyText(3, 'outranks the others')).toBe('Primacy 3: outranks the others')
  })
})

describe('fieldStateFromFunctions', () => {
  it('ticks and fills what the functions name, so buildFunctions returns the same functions', () => {
    const functions = { rowA: true, rowB: -3, rowC: { amount: 950, sign: false, scale: 2 }, rowD: 'cv7' }
    const state = fieldStateFromFunctions(rows, functions)
    expect(state.ticked).toEqual({ rowA: true, rowB: true, rowC: true, rowD: true })
    expect(state.values).toEqual({ rowA: true, rowB: '-3', rowC: { amount: '950', sign: false, scale: '2' }, rowD: 'cv7' })
    const built = buildFunctions(rows, state.ticked, state.values)
    expect(built.ok && built.functions).toEqual(functions)
  })

  it('leaves a row the functions omit unticked, and an optional object member unset', () => {
    const state = fieldStateFromFunctions(rows, { rowC: { amount: 5, sign: true } })
    expect(state.ticked).toEqual({ rowA: false, rowB: false, rowC: true, rowD: false })
    const built = buildFunctions(rows, state.ticked, state.values)
    expect(built.ok && built.functions).toEqual({ rowC: { amount: 5, sign: true } })
  })

  it('ignores a stored name the catalog lacks and a row of an unsupported kind', () => {
    const odd: CatalogRow = { name: 'rowE', description: 'odd', kind: 'matrix' }
    const state = fieldStateFromFunctions([flag, odd], { rowE: 1, gone: 2 })
    expect(state.ticked).toEqual({ rowA: false, rowE: false })
  })

  it('is the empty state when there are no functions', () => {
    expect(fieldStateFromFunctions(rows, undefined)).toEqual(emptyFieldState(rows))
  })
})

describe('loading the catalog and the curves', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })
  function reply(status: number, body: unknown) {
    return vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      json: () => Promise.resolve(body),
    } as Response)
  }

  it('reads the catalog rows from the catalog route', async () => {
    const spy = reply(200, { functions: [flag] })
    expect(await loadCatalog()).toEqual({ ok: true, data: [flag] })
    expect(String(spy.mock.calls[0]?.[0])).toBe('/api/der/control-functions')
  })

  it('reads a reply with no functions as empty and a failure as its error', async () => {
    reply(200, {})
    expect(await loadCatalog()).toEqual({ ok: true, data: [] })
    reply(500, { error: 'down' })
    expect(await loadCatalog()).toEqual({ ok: false, error: 'down' })
  })

  it('words a curve read failure with its cause', async () => {
    reply(500, { error: 'no store' })
    expect(await loadCurves()).toEqual({ ok: false, error: 'Stored curves could not be read: no store' })
    reply(200, { curves: [{ id: 'c', curveType: 7, description: 'd' }] })
    expect(await loadCurves()).toEqual({ ok: true, data: [{ id: 'c', curveType: 7, description: 'd' }] })
  })
})

describe('uneditableFunctions', () => {
  it('returns the stored names the catalog lacks and those of an unsupported kind, with their values', () => {
    const odd: CatalogRow = { name: 'rowE', description: 'odd', kind: 'matrix' }
    expect(uneditableFunctions([flag, odd], { rowA: true, rowE: [1], gone: 2 })).toEqual({ rowE: [1], gone: 2 })
    expect(uneditableFunctions([flag], { rowA: true })).toEqual({})
    expect(uneditableFunctions([flag], undefined)).toEqual({})
  })
})
