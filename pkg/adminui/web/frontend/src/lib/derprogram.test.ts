// The body builder mirrors the server's validation (internal/handler
// admin_derprogram.go buildProgramEdit), so each rule is pinned by the
// message the server would have sent. The request wrappers are asserted by
// the method, path and body that reach fetch, since a wrong verb or an
// unescaped id would only show against a live server.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  buildProgramBody,
  createDeviceProgram,
  createTemplate,
  deleteDeviceProgram,
  deleteTemplate,
  emptyForm,
  formFromProgram,
  groupByFsa,
  isDeviceEditable,
  listAllPrograms,
  listTemplates,
  originLabel,
  refusalOf,
  restartLabel,
  updateDeviceProgram,
  updateTemplate,
  type DefaultControlInput,
  type DefaultControlView,
  type ProgramBody,
  type ProgramFormValues,
  type ProgramListItem,
  type ProgramTemplate,
} from './derprogram'

afterEach(() => {
  vi.restoreAllMocks()
})

function form(over: Partial<ProgramFormValues> = {}): ProgramFormValues {
  return { ...emptyForm(), description: 'roof fleet', primacy: '65', ...over }
}

// What the form's ticked fields hand the builder: the functions a control
// form would send.
const CONNECT: DefaultControlInput = { ok: true, functions: { fnFlag: true } }
const create = (over: Partial<ProgramFormValues> = {}, defaultControl: DefaultControlInput = CONNECT) =>
  buildProgramBody(form(over), { requireDefault: true, defaultControl })

function fetchSpy(status: number, body: unknown) {
  return vi.spyOn(globalThis, 'fetch').mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: () => (body === undefined ? Promise.reject(new Error('empty')) : Promise.resolve(body)),
  } as Response)
}

describe('buildProgramBody', () => {
  it('sends the ticked functions and their values as the default control', () => {
    const functions = { fnFlag: true, fnNum: 5000, fnObj: { a: 950, b: true } }
    expect(create({}, { ok: true, functions })).toEqual({
      ok: true,
      body: { description: 'roof fleet', primacy: 65, defaultControl: { functions } },
    })
  })

  it('sends an mRID only when one was typed, trimmed', () => {
    const r = create({ mRID: '  0123456789abcdef0123456789abcdef ' })
    expect(r.ok && r.body.mRID).toBe('0123456789abcdef0123456789abcdef')
    const none = create()
    expect(none.ok && 'mRID' in none.body).toBe(false)
  })

  it.each<[string, Partial<ProgramFormValues>, string]>([
    ['blank description', { description: '' }, 'description: 1 to 32 printable ASCII characters required'],
    ['33-octet description', { description: 'x'.repeat(33) }, 'description: 1 to 32 printable ASCII characters required'],
    ['non-ASCII description', { description: 'caf\u00e9' }, 'description: 1 to 32 printable ASCII characters required'],
    ['blank primacy', { primacy: '' }, 'primacy: must be 0, 1, 2 or 65 to 191'],
    ['primacy 3', { primacy: '3' }, 'primacy: must be 0, 1, 2 or 65 to 191'],
    ['primacy 192', { primacy: '192' }, 'primacy: must be 0, 1, 2 or 65 to 191'],
    ['fractional primacy', { primacy: '65.5' }, 'primacy: must be 0, 1, 2 or 65 to 191'],
    ['short mRID', { mRID: 'abc' }, 'mRID: must be 32 hex digits, not the reserved form whose first 24 digits are F'],
    [
      'reserved mRID',
      { mRID: 'FFFFFFFFFFFFFFFFFFFFFFFF00000001' },
      'mRID: must be 32 hex digits, not the reserved form whose first 24 digits are F',
    ],
  ])('refuses %s with the server wording', (_name, over, message) => {
    expect(create(over)).toEqual({ ok: false, error: message })
  })

  it('refuses a create with nothing ticked, and shows why the functions could not be built', () => {
    expect(create({}, null)).toEqual({ ok: false, error: 'defaultControl: tick at least one function' })
    expect(create({}, { ok: false, error: 'fnNum must be a whole number.' })).toEqual({
      ok: false,
      error: 'fnNum must be a whole number.',
    })
  })

  it.each<[string, Partial<ProgramFormValues>, unknown]>([
    ['primacy 0', { primacy: '0' }, { primacy: 0 }],
    ['primacy 1', { primacy: '1' }, { primacy: 1 }],
    ['primacy 2', { primacy: '2' }, { primacy: 2 }],
    ['primacy 65', { primacy: '65' }, { primacy: 65 }],
    ['primacy 191', { primacy: '191' }, { primacy: 191 }],
    ['32-octet description', { description: 'x'.repeat(32) }, { description: 'x'.repeat(32) }],
    ['1-octet description', { description: 'x' }, { description: 'x' }],
    [
      'mRID just short of the reserved form',
      { mRID: 'FFFFFFFFFFFFFFFFFFFFFFFE00000001' },
      { mRID: 'FFFFFFFFFFFFFFFFFFFFFFFE00000001' },
    ],
  ])('accepts the edge value %s', (_name, over, expected) => {
    const r = create(over)
    expect(r.ok).toBe(true)
    expect(r.ok && r.body).toMatchObject(expected as object)
  })

  it.each<[string, string]>([
    ['a lowercase reserved mRID', 'ffffffffffffffffffffffff00000001'],
    ['a 33-digit mRID', '0123456789abcdef0123456789abcdef0'],
  ])('refuses %s', (_name, mRID) => {
    expect(create({ mRID }).ok).toBe(false)
  })

  it('omits the default control on an edit that ticks nothing, but sends what an edit ticks', () => {
    expect(buildProgramBody(form(), { requireDefault: false, defaultControl: null })).toEqual({
      ok: true,
      body: { description: 'roof fleet', primacy: 65 },
    })
    const r = buildProgramBody(form(), { requireDefault: false, defaultControl: CONNECT })
    expect(r.ok && r.body.defaultControl).toEqual({ functions: { fnFlag: true } })
  })

  it('sends an explicit null to remove the default when clearDefault is set', () => {
    const r = buildProgramBody(form(), { requireDefault: false, defaultControl: null, clearDefault: true })
    expect(r).toEqual({ ok: true, body: { description: 'roof fleet', primacy: 65, defaultControl: null } })
    expect(r.ok && 'defaultControl' in r.body).toBe(true)
  })
})

describe('formFromProgram', () => {
  it('reads the description and primacy and leaves the mRID blank', () => {
    const f = formFromProgram({ id: 'x', mRID: 'AB', description: 'd', primacy: 7, version: 1 })
    expect(f).toEqual({ description: 'd', primacy: '7', mRID: '' })
  })
})

describe('refusalOf', () => {
  it('keeps the code and the live controls of a delete conflict', () => {
    const r = refusalOf({
      ok: false,
      status: 409,
      error: 'cancel these controls, or wait for them to end, before deleting the program',
      body: { error: 'x', code: 'live_controls', controls: ['C1', 'C2'] },
    })
    expect(r).toEqual({
      status: 409,
      message: 'cancel these controls, or wait for them to end, before deleting the program',
      code: 'live_controls',
      controls: ['C1', 'C2'],
    })
  })

  it('has no code and no controls for a plain error', () => {
    expect(refusalOf({ ok: false, status: 404, error: 'fsa not found', body: { error: 'fsa not found' } })).toEqual({
      status: 404,
      message: 'fsa not found',
      code: undefined,
      controls: [],
    })
  })
})

describe('template requests', () => {
  it('lists, creates, updates and deletes against the escaped FSA path', async () => {
    const spy = fetchSpy(200, { templates: [] })
    await listTemplates('fsa/a')
    expect(spy.mock.calls[0][0]).toBe('/api/fsas/fsa%2Fa/der-programs')
    expect((spy.mock.calls[0][1] as RequestInit).method).toBe('GET')

    await createTemplate('fsa-a', { description: 'd', primacy: 1 })
    expect(spy.mock.calls[1][0]).toBe('/api/fsas/fsa-a/der-programs')
    expect((spy.mock.calls[1][1] as RequestInit).method).toBe('POST')

    await updateTemplate('fsa-a', 'derp-1', { description: 'e', primacy: 2 })
    expect(spy.mock.calls[2][0]).toBe('/api/fsas/fsa-a/der-programs/derp-1')
    expect((spy.mock.calls[2][1] as RequestInit).method).toBe('PUT')

    await deleteTemplate('fsa-a', 'derp-1')
    expect(spy.mock.calls[3][0]).toBe('/api/fsas/fsa-a/der-programs/derp-1')
    expect((spy.mock.calls[3][1] as RequestInit).method).toBe('DELETE')
  })
})

describe('device program requests', () => {
  it('uses the device routes, not the template routes', async () => {
    const spy = fetchSpy(200, {})
    await createDeviceProgram('7', 'fsa-a', { description: 'd', primacy: 1 })
    expect(spy.mock.calls[0][0]).toBe('/api/devices/7/fsas/fsa-a/der-programs')
    expect((spy.mock.calls[0][1] as RequestInit).method).toBe('POST')

    await updateDeviceProgram('7', 'derp-9', { description: 'd', primacy: 1 })
    expect(spy.mock.calls[1][0]).toBe('/api/devices/7/der-programs/derp-9')
    expect((spy.mock.calls[1][1] as RequestInit).method).toBe('PUT')

    await deleteDeviceProgram('7', 'derp-9')
    expect(spy.mock.calls[2][0]).toBe('/api/devices/7/der-programs/derp-9')
    expect((spy.mock.calls[2][1] as RequestInit).method).toBe('DELETE')
  })
})

describe('listAllPrograms', () => {
  const item = (id: string) => ({
    device: '7', fsa: 'fsa-a', id, href: `/edev/7/fsa/fsa-a/derp/${id}`, mRID: id, description: id,
    primacy: 1, version: 1, origin: 'template', onRestart: 'kept',
  })

  it('pages until every program is read and reports the persists flag', async () => {
    const pages = [
      { persists: true, total: 3, start: 0, limit: 2, programs: [item('a'), item('b')] },
      { persists: true, total: 3, start: 2, limit: 2, programs: [item('c')] },
    ]
    const spy = vi.spyOn(globalThis, 'fetch').mockImplementation(() =>
      Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(pages.shift()) } as Response),
    )

    const res = await listAllPrograms()

    expect(res.ok && res.data.programs.map((p) => p.id)).toEqual(['a', 'b', 'c'])
    expect(res.ok && res.data.persists).toBe(true)
    expect(spy.mock.calls.map((c) => c[0])).toEqual([
      '/api/der-programs?limit=500&start=0',
      '/api/der-programs?limit=500&start=2',
    ])
  })

  it('stops on an empty page instead of looping when the total over-counts', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ persists: false, total: 9, start: 0, limit: 500, programs: [] }),
    } as Response)

    const res = await listAllPrograms()

    expect(res.ok && res.data.programs).toEqual([])
    expect(res.ok && res.data.persists).toBe(false)
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('stops after 100 pages when the total never converges', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockImplementation(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ persists: true, total: 1e9, programs: [item('x')] }),
      } as Response),
    )

    const res = await listAllPrograms()

    expect(spy).toHaveBeenCalledTimes(100)
    expect(res.ok && res.data.programs).toHaveLength(100)
  })

  it('returns the refusal of a failed page as given', async () => {
    fetchSpy(500, { error: 'program list failed' })
    const res = await listAllPrograms()
    expect(res).toMatchObject({ ok: false, status: 500, error: 'program list failed' })
  })

  it('rejects a reply with no programs list rather than reading it as empty', async () => {
    fetchSpy(200, { persists: true, total: 1 })
    const res = await listAllPrograms()
    expect(res).toMatchObject({ ok: false, error: 'the program list reply has no programs list' })
  })
})

// The TypeScript wire shapes are written by hand, so each one is held to the
// json tags of the Go struct that serializes it. A renamed tag on either side
// fails here instead of surfacing as an undefined field in the panel.
describe('wire keys match the Go json tags', () => {
  const goSource = (name: string) =>
    readFileSync(resolve(import.meta.dirname, '../../../../../../internal/handler', name), 'utf8')
  const derprogramGo = goSource('admin_derprogram.go')
  const derprogramListGo = goSource('admin_derprogram_list.go')

  function tags(source: string, struct: string): string[] {
    const m = new RegExp(`type ${struct} struct \\{([\\s\\S]*?)\\n\\}`).exec(source)
    if (!m) throw new Error(`struct ${struct} not found`)
    return [...m[1].matchAll(/json:"([^",]+)/g)].map((t) => t[1]).sort()
  }

  // A Record over keyof makes the compiler reject a key added to or dropped
  // from the interface without updating this list.
  const templateKeys: Record<keyof ProgramTemplate, true> = {
    id: true, mRID: true, description: true, primacy: true, version: true, defaultControl: true, copies: true,
  }
  const listItemKeys: Record<keyof ProgramListItem, true> = {
    device: true, fsa: true, id: true, href: true, mRID: true, description: true, primacy: true, version: true,
    origin: true, templateId: true, onRestart: true,
  }
  const bodyKeys: Record<keyof Required<ProgramBody>, true> = {
    description: true, primacy: true, mRID: true, defaultControl: true,
  }
  const defaultControlKeys: Record<keyof DefaultControlView, true> = {
    mRID: true, type: true, functions: true, derControlBase: true,
  }
  const keys = (r: Record<string, true>) => Object.keys(r).sort()

  it('ProgramTemplate, DefaultControlView and ProgramBody match admin_derprogram.go', () => {
    expect(keys(templateKeys)).toEqual(tags(derprogramGo, 'ProgramTemplateView'))
    expect(keys(defaultControlKeys)).toEqual(tags(derprogramGo, 'ProgramDefaultControlView'))
    expect(keys(bodyKeys)).toEqual(tags(derprogramGo, 'programTemplateBody'))
  })

  it('ProgramListItem matches admin_derprogram_list.go', () => {
    expect(keys(listItemKeys)).toEqual(tags(derprogramListGo, 'ProgramListItem'))
  })

  it('reads the Go sources it compares against (the parser can fail)', () => {
    expect(tags(derprogramListGo, 'ProgramListReply')).toEqual(['limit', 'persists', 'programs', 'start', 'total'])
    expect(() => tags(derprogramGo, 'NoSuchStruct')).toThrow('not found')
  })
})

describe('program origin helpers', () => {
  const at = (over: Partial<ProgramListItem>): ProgramListItem => ({
    device: '7', fsa: 'fsa-a', id: 'p', href: '/edev/7/fsa/fsa-a/derp/p', mRID: 'M', description: 'd',
    primacy: 1, version: 1, origin: 'device', onRestart: 'kept', ...over,
  })

  it('offers device edit for device and fixture programs only', () => {
    expect(isDeviceEditable({ origin: 'device' })).toBe(true)
    expect(isDeviceEditable({ origin: 'fixture' })).toBe(true)
    expect(isDeviceEditable({ origin: 'template' })).toBe(false)
    expect(isDeviceEditable({ origin: 'imported' })).toBe(false)
  })

  it('words the known origins and restart values, and passes others through', () => {
    expect(originLabel('template')).toBe('Template copy')
    expect(originLabel('fixture')).toBe('Fixture')
    expect(originLabel('device')).toBe('Device')
    expect(originLabel('other')).toBe('other')
    expect(restartLabel('kept')).toBe('Kept on restart')
    expect(restartLabel('reseeded')).toBe('Reseeded on restart')
    expect(restartLabel('gone')).toBe('Lost on restart')
    expect(restartLabel('later')).toBe('later')
  })

  it('groups one device by FSA in first-seen order and adds assigned FSAs with no program', () => {
    const groups = groupByFsa(
      [at({ id: 'a' }), at({ id: 'x', device: '8' }), at({ id: 'b', fsa: 'fsa-b' }), at({ id: 'c' })],
      '7',
      ['fsa-b', 'fsa-z'],
    )
    expect(groups.map((g) => [g.fsa, g.programs.map((p) => p.id)])).toEqual([
      ['fsa-a', ['a', 'c']],
      ['fsa-b', ['b']],
      ['fsa-z', []],
    ])
  })
})
