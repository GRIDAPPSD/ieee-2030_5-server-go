// Tests run against a fake fetch keyed by method and path, so each asserts
// the request that would reach the server and the text the operator sees,
// including a refusal shown as the server sent it. The stale-response and
// teardown guards are pinned by holding a reply open and moving on.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import DevicePrograms from './DevicePrograms.svelte'
import { CATALOG_ROUTES } from '../lib/controlcatalog.fixture'
import type { ProgramListItem } from '../lib/derprogram'
import type { AdminFSA } from '../lib/fsa'

afterEach(() => {
  vi.restoreAllMocks()
})

function item(over: Partial<ProgramListItem> = {}): ProgramListItem {
  const id = over.id ?? 'derp-1'
  return {
    device: '7',
    fsa: 'fsa-a',
    id,
    href: `/edev/7/fsa/fsa-a/derp/${id}`,
    mRID: 'ABCDEF0123456789ABCDEF0123456789',
    description: `program ${id}`,
    primacy: 65,
    version: 3,
    origin: 'device',
    onRestart: 'kept',
    ...over,
  }
}

function fsa(over: Partial<AdminFSA> = {}): AdminFSA {
  return { href: '/api/fsas/fsa-a', mRID: 'fsa-a', description: 'roof', primacy: 0, devices: ['7'], ...over }
}

interface Reply {
  status: number
  body?: unknown
}
interface Call {
  method: string
  path: string
  body: unknown
  signal: AbortSignal | null | undefined
}

function fake(routes: Record<string, Reply | (() => Reply | Promise<Reply>)>) {
  const calls: Call[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => {
    const method = init?.method ?? 'GET'
    const path = String(input)
    calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined, signal: init?.signal })
    const hit = { ...CATALOG_ROUTES, ...routes }[`${method} ${path}`]
    const reply = typeof hit === 'function' ? hit() : (hit ?? { status: 404, body: { error: 'no route' } })
    return Promise.resolve(reply).then(
      (r) =>
        ({
          ok: r.status >= 200 && r.status < 300,
          status: r.status,
          json: () => (r.body === undefined ? Promise.reject(new Error('empty')) : Promise.resolve(r.body)),
        }) as Response,
    )
  })
  return calls
}

const LIST = 'GET /api/der-programs?limit=500&start=0'

function catalog(programs: ProgramListItem[], persists = true): Reply {
  return { status: 200, body: { persists, total: programs.length, start: 0, limit: 500, programs } }
}

function mount(props: { deviceId?: string; fsas?: AdminFSA[]; onClose?: () => void; onFsaChanged?: () => void } = {}) {
  return render(DevicePrograms, {
    props: { deviceId: '7', fsas: [fsa()], onClose: () => {}, onFsaChanged: () => {}, ...props },
  })
}

const THREE = [
  item({ id: 'derp-t', origin: 'template', templateId: 'derp-t', description: 'from template' }),
  item({ id: 'derp-f', origin: 'fixture', onRestart: 'reseeded', description: 'from fixture' }),
  item({ id: 'derp-d', origin: 'device', description: 'on this device' }),
]

describe('DevicePrograms rows', () => {
  it('shows each origin with its badge, restart note, version and the right actions', async () => {
    fake({ [LIST]: catalog(THREE) })
    mount()

    const t = await screen.findByTestId('program-row-derp-t')
    expect(within(t).getByTestId('program-origin')).toHaveTextContent('Template copy')
    expect(within(t).getByTestId('program-restart')).toHaveTextContent('Kept on restart')
    expect(within(t).getByTestId('program-version')).toHaveTextContent('3')
    expect(within(t).getByTestId('program-primacy')).toHaveTextContent('65')
    expect(within(t).getByTestId('program-description')).toHaveTextContent('from template')
    expect(within(t).queryByTestId('program-edit-derp-t')).toBeNull()
    expect(within(t).queryByTestId('program-delete-derp-t')).toBeNull()
    expect(within(t).getByTestId('program-fsa-link-derp-t')).toHaveAttribute('href', '/ui/fsas')

    const f = screen.getByTestId('program-row-derp-f')
    expect(within(f).getByTestId('program-origin')).toHaveTextContent('Fixture')
    expect(within(f).getByTestId('program-restart')).toHaveTextContent('Reseeded on restart')
    expect(within(f).getByTestId('program-edit-derp-f')).toBeInTheDocument()
    expect(within(f).getByTestId('program-delete-derp-f')).toBeInTheDocument()
    expect(within(f).queryByTestId('program-fsa-link-derp-f')).toBeNull()

    const d = screen.getByTestId('program-row-derp-d')
    expect(within(d).getByTestId('program-origin')).toHaveTextContent('Device')
    expect(within(d).getByTestId('program-edit-derp-d')).toBeInTheDocument()
    expect(within(d).getByTestId('program-delete-derp-d')).toBeInTheDocument()
  })

  it('shows an origin or restart value it does not know exactly as sent', async () => {
    fake({ [LIST]: catalog([item({ origin: 'imported', onRestart: 'sometimes' })]) })
    mount()
    const row = await screen.findByTestId('program-row-derp-1')
    expect(within(row).getByTestId('program-origin')).toHaveTextContent('imported')
    expect(within(row).getByTestId('program-restart')).toHaveTextContent('sometimes')
    expect(within(row).queryByTestId('program-edit-derp-1')).toBeNull()
  })

  it('shows only this device, grouped by FSA, and an assigned FSA with no program', async () => {
    fake({
      [LIST]: catalog([
        item({ id: 'a1' }),
        item({ id: 'b1', fsa: 'fsa-b', href: '/edev/7/fsa/fsa-b/derp/b1' }),
        item({ id: 'other', device: '8', href: '/edev/8/fsa/fsa-a/derp/other' }),
      ]),
    })
    mount({ fsas: [fsa(), fsa({ mRID: 'fsa-b', description: 'ground' }), fsa({ mRID: 'fsa-c', description: 'empty one' })] })

    await screen.findByTestId('program-row-a1')
    expect(within(screen.getByTestId('programs-fsa-fsa-a')).getByTestId('program-row-a1')).toBeInTheDocument()
    expect(within(screen.getByTestId('programs-fsa-fsa-b')).getByTestId('program-row-b1')).toBeInTheDocument()
    expect(screen.queryByTestId('program-row-other')).toBeNull()
    // fsa-c lists device 7 too, so it can take a device program.
    expect(screen.getByTestId('programs-fsa-fsa-c')).toHaveTextContent('empty one')
    expect(screen.getByTestId('program-add-fsa-c')).toBeInTheDocument()
  })

  it('says the device has no programs only after a successful read', async () => {
    fake({ [LIST]: catalog([], true) })
    mount({ fsas: [] })
    expect(await screen.findByTestId('programs-empty')).toBeInTheDocument()
  })

  it('shows the error, not an empty list, when the read fails', async () => {
    fake({ [LIST]: { status: 500, body: { error: 'program list failed' } } })
    mount()
    expect(await screen.findByTestId('programs-error')).toHaveTextContent('Could not load programs (500): program list failed')
    expect(screen.queryByTestId('programs-empty')).toBeNull()
  })
})

describe('DevicePrograms persistence banner', () => {
  it('warns that nothing survives a restart when the server persists nothing', async () => {
    fake({ [LIST]: catalog(THREE, false) })
    mount()
    expect(await screen.findByTestId('programs-persist-banner')).toHaveTextContent('nothing here survives a restart')
  })

  it('shows no banner when the server persists', async () => {
    fake({ [LIST]: catalog(THREE, true) })
    mount()
    await screen.findByTestId('program-row-derp-t')
    expect(screen.queryByTestId('programs-persist-banner')).toBeNull()
  })
})

describe('DevicePrograms add', () => {
  it('posts a device program to its FSA route, then reloads', async () => {
    let list = catalog([])
    const calls = fake({
      [LIST]: () => list,
      'POST /api/devices/7/fsas/fsa-a/der-programs': () => {
        list = catalog([item({ id: 'new1', description: 'added' })])
        return { status: 201, body: {} }
      },
    })
    mount()

    await fireEvent.click(await screen.findByTestId('program-add-fsa-a'))
    await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'added' } })
    await fireEvent.input(screen.getByLabelText('Primacy'), { target: { value: '1' } })
    await fireEvent.click(await screen.findByTestId('control-fn-fnFlag'))
    await fireEvent.click(screen.getByTestId('control-fv-fnFlag'))
    await fireEvent.click(screen.getByTestId('control-fn-fnLevel'))
    await fireEvent.input(screen.getByTestId('control-fv-fnLevel'), { target: { value: '4000' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add device program' }))

    expect(await screen.findByTestId('program-row-new1')).toBeInTheDocument()
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.body).toEqual({
      description: 'added',
      primacy: 1,
      defaultControl: { functions: { fnFlag: true, fnLevel: 4000 } },
    })
    expect(screen.queryByTestId('program-form')).toBeNull()
  })

  it('shows an add refusal as the server sent it and keeps the form', async () => {
    fake({
      [LIST]: catalog([]),
      'POST /api/devices/7/fsas/fsa-a/der-programs': { status: 409, body: { error: 'the device holds the most programs allowed' } },
    })
    mount()
    await fireEvent.click(await screen.findByTestId('program-add-fsa-a'))
    await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'x' } })
    await fireEvent.input(screen.getByLabelText('Primacy'), { target: { value: '1' } })
    await fireEvent.click(await screen.findByTestId('control-fn-fnFlag'))
    await fireEvent.click(screen.getByRole('button', { name: 'Add device program' }))

    expect(await screen.findByTestId('program-form-error')).toHaveTextContent(
      'Error (409): the device holds the most programs allowed',
    )
    expect(screen.getByTestId('program-form')).toBeInTheDocument()
  })
})

describe('DevicePrograms edit', () => {
  it('puts the edit to the device route for a device or fixture program', async () => {
    let list = catalog(THREE)
    const calls = fake({
      [LIST]: () => list,
      'PUT /api/devices/7/der-programs/derp-f': () => {
        list = catalog([item({ id: 'derp-f', origin: 'fixture', description: 'renamed', version: 4 })])
        return { status: 200, body: {} }
      },
    })
    mount()

    await fireEvent.click(await screen.findByTestId('program-edit-derp-f'))
    expect(screen.getByLabelText('Description')).toHaveValue('from fixture')
    await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'renamed' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Save device program' }))

    await waitFor(() => expect(within(screen.getByTestId('program-row-derp-f')).getByTestId('program-version')).toHaveTextContent('4'))
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ description: 'renamed', primacy: 65 })
    expect(screen.queryByTestId('program-form')).toBeNull()
  })

  it('shows the server refusal of an edit as sent, such as a program that turned out to be a template copy', async () => {
    fake({
      [LIST]: catalog(THREE),
      'PUT /api/devices/7/der-programs/derp-d': {
        status: 409,
        body: { error: 'this program is a copy of a template; edit the template on the FSAs tab', code: 'edit_the_template' },
      },
    })
    mount()
    await fireEvent.click(await screen.findByTestId('program-edit-derp-d'))
    await fireEvent.click(screen.getByRole('button', { name: 'Save device program' }))

    expect(await screen.findByTestId('program-form-error')).toHaveTextContent(
      'Error (409): this program is a copy of a template; edit the template on the FSAs tab',
    )
  })
})

describe('DevicePrograms delete', () => {
  it('asks to confirm, then deletes on the device route and reloads', async () => {
    let list = catalog(THREE)
    const calls = fake({
      [LIST]: () => list,
      'DELETE /api/devices/7/der-programs/derp-d': () => {
        list = catalog(THREE.slice(0, 2))
        return { status: 204 }
      },
    })
    mount()

    await fireEvent.click(await screen.findByTestId('program-delete-derp-d'))
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await fireEvent.click(screen.getByTestId('program-delete-confirm-derp-d'))

    await waitFor(() => expect(screen.queryByTestId('program-row-derp-d')).toBeNull())
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(1)
  })

  it('sends one delete however many times Confirm is pressed while it is out', async () => {
    let release: (r: Reply) => void = () => {}
    const calls = fake({
      [LIST]: catalog(THREE),
      'DELETE /api/devices/7/der-programs/derp-d': () => new Promise<Reply>((res) => (release = res)),
    })
    mount()
    await fireEvent.click(await screen.findByTestId('program-delete-derp-d'))
    const confirm = screen.getByTestId('program-delete-confirm-derp-d')
    await fireEvent.click(confirm)
    await fireEvent.click(confirm)
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(1)
    expect(confirm).toBeDisabled()
    release({ status: 204 })
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET').length).toBeGreaterThan(1))
  })

  it('shows a refusal as sent, with the live controls that blocked it', async () => {
    fake({
      [LIST]: catalog(THREE),
      'DELETE /api/devices/7/der-programs/derp-d': {
        status: 409,
        body: { error: 'the program has live controls', code: 'live_controls', controls: ['ctl-1', 'ctl-2'] },
      },
    })
    mount()
    await fireEvent.click(await screen.findByTestId('program-delete-derp-d'))
    await fireEvent.click(screen.getByTestId('program-delete-confirm-derp-d'))

    const box = await screen.findByTestId('program-conflict-derp-d')
    expect(box).toHaveTextContent('the program has live controls')
    expect(box).toHaveTextContent('ctl-1')
    expect(box).toHaveTextContent('ctl-2')
    expect(screen.getByTestId('program-row-derp-d')).toBeInTheDocument()
  })

  it('shows any other delete refusal with its status and message', async () => {
    fake({
      [LIST]: catalog(THREE),
      'DELETE /api/devices/7/der-programs/derp-d': { status: 500, body: { error: 'program write failed' } },
    })
    mount()
    await fireEvent.click(await screen.findByTestId('program-delete-derp-d'))
    await fireEvent.click(screen.getByTestId('program-delete-confirm-derp-d'))
    expect(await screen.findByTestId('program-delete-error-derp-d')).toHaveTextContent('Delete failed (500): program write failed')
  })
})

describe('DevicePrograms stale replies and teardown', () => {
  it('ignores an older read that lands after the device changed', async () => {
    let releaseOld: (r: Reply) => void = () => {}
    let n = 0
    fake({
      [LIST]: () => {
        n++
        if (n === 1) return new Promise<Reply>((res) => (releaseOld = res))
        return catalog([item({ id: 'new-dev', device: '8', href: '/edev/8/fsa/fsa-a/derp/new-dev' })])
      },
    })
    const view = mount({ deviceId: '7', fsas: [] })
    await view.rerender({ deviceId: '8', fsas: [], onClose: () => {} })
    expect(await screen.findByTestId('program-row-new-dev')).toBeInTheDocument()

    releaseOld(catalog([item({ id: 'old-dev', device: '8', href: '/edev/8/fsa/fsa-a/derp/old-dev', description: 'stale' })]))
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByTestId('program-row-old-dev')).toBeNull()
    expect(screen.getByTestId('program-row-new-dev')).toBeInTheDocument()
  })

  it('aborts the read when the drawer closes, and a late reply changes nothing', async () => {
    let release: (r: Reply) => void = () => {}
    const calls = fake({ [LIST]: () => new Promise<Reply>((res) => (release = res)) })
    const view = mount()
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].signal?.aborted).toBe(false)

    view.unmount()
    expect(calls[0].signal?.aborted).toBe(true)
    release(catalog(THREE))
    await new Promise((r) => setTimeout(r, 0))
    expect(calls).toHaveLength(1)
  })

  it('does not reload after the drawer closes while a delete is out', async () => {
    let release: (r: Reply) => void = () => {}
    const calls = fake({
      [LIST]: catalog(THREE),
      'DELETE /api/devices/7/der-programs/derp-d': () => new Promise<Reply>((res) => (release = res)),
    })
    const view = mount()
    await fireEvent.click(await screen.findByTestId('program-delete-derp-d'))
    await fireEvent.click(screen.getByTestId('program-delete-confirm-derp-d'))
    const before = calls.length

    view.unmount()
    release({ status: 204 })
    await new Promise((r) => setTimeout(r, 0))
    expect(calls).toHaveLength(before)
  })

  it('shows the answer to the newest read when a reload overlaps an older one', async () => {
    let releaseSecond: (r: Reply) => void = () => {}
    let n = 0
    const calls = fake({
      [LIST]: () => {
        n++
        if (n === 1) return catalog([item({ id: 'derp-d' })])
        if (n === 2) return new Promise<Reply>((res) => (releaseSecond = res))
        return catalog([item({ id: 'fresh' })])
      },
      'POST /api/devices/7/fsas/fsa-a/der-programs': { status: 201, body: {} },
      'DELETE /api/devices/7/der-programs/derp-d': { status: 204 },
    })
    mount()
    // An add reloads the list; that second read is held open.
    await fireEvent.click(await screen.findByTestId('program-add-fsa-a'))
    await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'x' } })
    await fireEvent.input(screen.getByLabelText('Primacy'), { target: { value: '1' } })
    await fireEvent.click(await screen.findByTestId('control-fn-fnFlag'))
    await fireEvent.click(screen.getByRole('button', { name: 'Add device program' }))
    await waitFor(() => expect(n).toBe(2))
    // A delete meanwhile reloads again, and that third read answers first.
    await fireEvent.click(screen.getByTestId('program-delete-derp-d'))
    await fireEvent.click(screen.getByTestId('program-delete-confirm-derp-d'))
    expect(await screen.findByTestId('program-row-fresh')).toBeInTheDocument()

    releaseSecond(catalog([item({ id: 'stale' })]))
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByTestId('program-row-stale')).toBeNull()
    expect(screen.getByTestId('program-row-fresh')).toBeInTheDocument()
    expect(calls.filter((c) => `${c.method} ${c.path}` === LIST)).toHaveLength(3)
  })
})

describe('DevicePrograms close', () => {
  it('tells the caller when Close is pressed', async () => {
    fake({ [LIST]: catalog(THREE) })
    const onClose = vi.fn()
    mount({ onClose })
    await screen.findByTestId('program-row-derp-t')
    await fireEvent.click(screen.getByRole('button', { name: 'Close programs' }))
    expect(onClose).toHaveBeenCalledOnce()
  })
})

describe('DevicePrograms unassign from FSA', () => {
  it('names the FSA, asks to confirm, then deletes the assignment by mRID and reloads', async () => {
    let list = catalog(THREE)
    const calls = fake({
      [LIST]: () => list,
      'DELETE /api/devices/7/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-a': () => {
        list = catalog([])
        return { status: 200, body: { device: '7' } }
      },
    })
    const onFsaChanged = vi.fn()
    mount({ onFsaChanged })

    const button = await screen.findByTestId('fsa-unassign-fsa-a')
    expect(button).toHaveAccessibleName('Unassign from FSA roof (fsa-a)')
    await fireEvent.click(button)
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await fireEvent.click(screen.getByTestId('fsa-unassign-confirm-fsa-a'))

    await waitFor(() => expect(screen.queryByTestId('program-row-derp-t')).toBeNull())
    const dels = calls.filter((c) => c.method === 'DELETE')
    expect(dels.map((c) => c.path)).toEqual(['/api/devices/7/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-a'])
    expect(onFsaChanged).toHaveBeenCalledTimes(1)
  })

  it('Keep sends nothing and closes the confirmation', async () => {
    const calls = fake({ [LIST]: catalog(THREE) })
    mount()
    await fireEvent.click(await screen.findByTestId('fsa-unassign-fsa-a'))
    await fireEvent.click(screen.getByRole('button', { name: 'Keep assignment to roof (fsa-a)' }))
    expect(screen.getByTestId('fsa-unassign-fsa-a')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
  })

  it('shows a 409 as sent, naming the blocking program, and keeps the programs', async () => {
    const onFsaChanged = vi.fn()
    fake({
      [LIST]: catalog(THREE),
      'DELETE /api/devices/7/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-a': {
        status: 409,
        body: {
          error: 'device programs sit under this fsa and must be deleted before the device is unassigned: derp-d',
          code: 'device_programs_present',
          programs: ['derp-d'],
        },
      },
    })
    mount({ onFsaChanged })
    await fireEvent.click(await screen.findByTestId('fsa-unassign-fsa-a'))
    await fireEvent.click(screen.getByTestId('fsa-unassign-confirm-fsa-a'))

    expect(await screen.findByTestId('fsa-unassign-error-fsa-a')).toHaveTextContent(
      'Unassign failed (409): device programs sit under this fsa and must be deleted before the device is unassigned: derp-d',
    )
    expect(screen.getByTestId('program-row-derp-d')).toBeInTheDocument()
    expect(onFsaChanged).not.toHaveBeenCalled()
  })
})
