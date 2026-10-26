import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import DerControl from './DerControl.svelte'
import * as api from '../lib/api'
import type { AdminFSA } from '../lib/fsa'
import type { DashboardDevice } from '../lib/dashboard'
import type { CatalogResponse } from '../lib/controlcatalog'
import type { FsaControlCreated, FsaControlView } from '../lib/fsacontrol'
import type { ProgramTemplate } from '../lib/derprogram'

// The FSA target of the Control window: the same form as the device target,
// addressed to an FSA's program template.

const fsas: AdminFSA[] = [
  { href: '/api/fsas/fsa-A', mRID: 'fsa-A', description: 'feeder A', primacy: 1, devices: ['d1', 'd2'] },
  { href: '/api/fsas/fsa-B', mRID: 'fsa-B', description: 'feeder B', primacy: 1, devices: [] },
]

const catalog: CatalogResponse = {
  functions: [{ name: 'fnFlag', description: 'a flag', kind: 'boolean' }],
}

function template(over: Partial<ProgramTemplate> = {}): ProgramTemplate {
  return {
    id: 't1',
    mRID: 'MT1',
    description: 'tmpl one',
    primacy: 5,
    version: 1,
    defaultControl: { mRID: 'DD', type: 'combined', derControlBase: { fnOld: 3 } as never, functions: { fnOld: 3 } },
    copies: ['d1', 'd2'],
    ...over,
  }
}

function fsaControl(over: Partial<FsaControlView> = {}): FsaControlView {
  const held = { currentStatus: 1, status: 'scheduled', dateTime: 10 }
  return {
    id: 'c1',
    mRID: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
    type: 'fnFlag',
    description: '',
    derControlBase: {},
    functions: { fnFlag: true },
    creationTime: 1000,
    interval: { start: 5000, duration: 300 },
    ended: false,
    cancelled: false,
    cancelledAt: null,
    copies: [
      { device: 'd1', href: '/edev/d1/fsa/fsa-A/derp/t1/derc/c1', eventStatus: held },
      { device: 'd2', href: '/edev/d2/fsa/fsa-A/derp/t1/derc/c1', eventStatus: held },
    ],
    ...over,
  }
}

interface World {
  templates: ProgramTemplate[]
  controls: FsaControlView[]
  templatesReply?: unknown
}

function mockWorld(w: World) {
  const get = vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    const url = new URL(path, 'http://x')
    if (url.pathname === '/api/der/control-functions') return { ok: true, data: catalog }
    if (url.pathname === '/api/der/curves') return { ok: true, data: { curves: [] } }
    if (url.pathname === '/api/fsas') return { ok: true, data: { fsas } }
    if (url.pathname === '/api/fsas/fsa-A/der-programs') return w.templatesReply ?? { ok: true, data: { templates: w.templates } }
    if (url.pathname === '/api/fsas/fsa-B/der-programs') return { ok: true, data: { templates: [] } }
    if (url.pathname === '/api/fsas/fsa-A/der-programs/t1/controls') {
      return { ok: true, data: { fsa: 'fsa-A', program: 't1', controls: w.controls } }
    }
    return { ok: false, error: 'unexpected path: ' + path, status: 404 }
  }) as never)
  const post = vi.spyOn(api, 'postJSON')
  const put = vi.spyOn(api, 'putJSON')
  return { get, post, put }
}

async function mountFsa(w: World) {
  const mocks = mockWorld(w)
  const view = render(DerControl, { devices: [] })
  await screen.findByTestId('control-fn-fnFlag')
  await fireEvent.click(screen.getByTestId('control-target-fsa'))
  await waitFor(() => expect(screen.getByRole('option', { name: 'feeder A' })).toBeInTheDocument())
  return { ...mocks, container: view.container }
}

async function pickProgram(c: HTMLElement, fsa = 'fsa-A', program = 't1') {
  await fireEvent.change(c.querySelector('#controlFsa') as HTMLSelectElement, { target: { value: fsa } })
  await waitFor(() => expect(screen.getByRole('option', { name: 'tmpl one' })).toBeInTheDocument())
  await fireEvent.change(c.querySelector('#controlFsaProgram') as HTMLSelectElement, { target: { value: program } })
  await screen.findByTestId('fsa-control-default-row')
}

const tid = (id: string) => screen.getByTestId(id)

describe('DerControl, FSA target: a scheduled control', () => {
  it('Send posts nothing; Confirm posts the FSA control route once, without a kind, and reports the devices holding it', async () => {
    const w: World = { templates: [template()], controls: [] }
    const { post, container } = await mountFsa(w)
    post.mockImplementation((async () => {
      const created: FsaControlCreated = { ...fsaControl(), notificationAttempted: true, persisted: true }
      w.controls = [created]
      return { ok: true, data: created }
    }) as never)
    await pickProgram(container)
    await fireEvent.click(tid('control-fn-fnFlag'))
    await fireEvent.click(tid('control-fv-fnFlag'))
    await fireEvent.input(container.querySelector('#controlDuration') as HTMLInputElement, { target: { value: '5' } })

    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(post).not.toHaveBeenCalled()
    expect(tid('der-control-confirm')).toHaveTextContent('Schedule fnFlag=true on FSA fsa-A, program tmpl one, starting now, for 5 min.')

    const confirm = await screen.findByRole('button', { name: 'Confirm' })
    await fireEvent.click(confirm)
    await fireEvent.click(confirm)
    expect(post).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledWith('/api/fsas/fsa-A/der-programs/t1/controls', { functions: { fnFlag: true }, durationSeconds: 300 })
    expect(Object.keys(post.mock.calls[0]?.[1] as object)).not.toContain('kind')

    await waitFor(() => expect(container.querySelector('#controlResult')).toHaveTextContent('Held by 2 of 2 devices.'))
    const row = await screen.findByTestId('fsa-control-row')
    expect(row).toHaveAttribute('data-mrid', 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')
    expect(tid('fsa-control-holding')).toHaveTextContent('2 of 2 devices')
    expect(tid('fsa-control-status')).toHaveTextContent('scheduled')
  })

  it('counts only the devices that hold a copy', async () => {
    const partial = fsaControl({
      copies: [
        { device: 'd1', href: 'h1', eventStatus: { currentStatus: 1, status: 'scheduled', dateTime: 10 } },
        { device: 'd2', href: 'h2', eventStatus: null },
      ],
    })
    const { container } = await mountFsa({ templates: [template()], controls: [partial] })
    await pickProgram(container)
    expect(await screen.findByTestId('fsa-control-holding')).toHaveTextContent('1 of 2 devices')
  })

  it('refuses a send with no program picked, posting nothing', async () => {
    const { post, put, container } = await mountFsa({ templates: [template()], controls: [] })
    await fireEvent.click(tid('control-fn-fnFlag'))
    await fireEvent.input(container.querySelector('#controlDuration') as HTMLInputElement, { target: { value: '5' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(container.querySelector('#controlResult')).toHaveTextContent('Pick an FSA and one of its programs first.')
    expect(post).not.toHaveBeenCalled()
    expect(put).not.toHaveBeenCalled()
  })

  it('shows a refusal from the FSA route and still posts nothing to the device route', async () => {
    const { post, container } = await mountFsa({ templates: [template()], controls: [] })
    post.mockResolvedValue({ ok: false, error: 'a device holds no copy', status: 409, body: { code: 'copy_missing' } } as never)
    await pickProgram(container)
    await fireEvent.click(tid('control-fn-fnFlag'))
    await fireEvent.input(container.querySelector('#controlDuration') as HTMLInputElement, { target: { value: '5' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(container.querySelector('#controlResult')).toHaveTextContent('Error: a device holds no copy'))
    expect(post.mock.calls.map((c) => c[0])).toEqual(['/api/fsas/fsa-A/der-programs/t1/controls'])
  })
})

describe('DerControl, FSA target: a default', () => {
  it('saves through the template edit route with the stored description and primacy, and posts no control', async () => {
    const w: World = { templates: [template()], controls: [] }
    const { post, put, container } = await mountFsa(w)
    put.mockImplementation((async () => {
      w.templates = [template({ version: 2, defaultControl: { mRID: 'DD', type: 'combined', derControlBase: { fnNew: 7 } as never, functions: { fnNew: 7 } } })]
      return { ok: true, data: w.templates[0] }
    }) as never)
    await pickProgram(container)
    expect(tid('fsa-control-default-row')).toHaveTextContent('fnOld=3')

    await fireEvent.click(tid('control-mode-default'))
    await fireEvent.click(tid('control-fn-fnFlag'))
    await fireEvent.click(tid('control-fv-fnFlag'))
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(put).not.toHaveBeenCalled()
    expect(tid('der-control-confirm')).toHaveTextContent('Set the default control fnFlag=true on FSA fsa-A, program tmpl one.')
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))

    expect(put).toHaveBeenCalledTimes(1)
    expect(put).toHaveBeenCalledWith('/api/fsas/fsa-A/der-programs/t1', {
      description: 'tmpl one',
      primacy: 5,
      defaultControl: { functions: { fnFlag: true } },
    })
    expect(post).not.toHaveBeenCalled()
    await waitFor(() => expect(container.querySelector('#controlResult')).toHaveTextContent('Stored the default control on program tmpl one of FSA fsa-A.'))
    await waitFor(() => expect(tid('fsa-control-default-row')).toHaveTextContent('fnNew=7'))
  })
})

describe('DerControl, FSA target: cancel', () => {
  it('asks to confirm, posts the FSA cancel route with the reason, and the row reads cancelled with no Cancel left', async () => {
    const w: World = { templates: [template()], controls: [fsaControl()] }
    const { post, container } = await mountFsa(w)
    post.mockImplementation((async () => {
      w.controls = [fsaControl({ cancelled: true, cancelledAt: 2000, cancelReason: 'storm' })]
      return { ok: true, data: w.controls[0] }
    }) as never)
    await pickProgram(container)
    const mrid = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'

    await fireEvent.click(await screen.findByTestId(`der-control-cancel-${mrid}`))
    expect(post).not.toHaveBeenCalled()
    await fireEvent.input(screen.getByPlaceholderText('reason (optional)'), { target: { value: 'storm' } })
    await fireEvent.click(tid(`der-control-confirm-cancel-${mrid}`))

    expect(post).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledWith('/api/fsas/fsa-A/der-programs/t1/controls/c1/cancel', { reason: 'storm' })
    await waitFor(() => expect(tid('fsa-control-status')).toHaveTextContent('cancelled'))
    expect(screen.queryByTestId(`der-control-cancel-${mrid}`)).toBeNull()
  })

  it('offers no Cancel on an ended control', async () => {
    const { container } = await mountFsa({ templates: [template()], controls: [fsaControl({ ended: true })] })
    await pickProgram(container)
    expect(await screen.findByTestId('fsa-control-status')).toHaveTextContent('ended')
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
  })

  it('shows a failed cancel under the table', async () => {
    const { post, container } = await mountFsa({ templates: [template()], controls: [fsaControl()] })
    post.mockResolvedValue({ ok: false, error: 'cancel stopped at device d2', status: 500 } as never)
    await pickProgram(container)
    const mrid = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
    await fireEvent.click(await screen.findByTestId(`der-control-cancel-${mrid}`))
    await fireEvent.click(tid(`der-control-confirm-cancel-${mrid}`))
    expect(await screen.findByTestId('der-control-cancel-result')).toHaveTextContent('Cancel failed: cancel stopped at device d2')
  })
})

describe('DerControl, FSA target: program choice', () => {
  it('says so when the FSA has no program template, and a failed read is named', async () => {
    const { container } = await mountFsa({ templates: [], controls: [] })
    await fireEvent.change(container.querySelector('#controlFsa') as HTMLSelectElement, { target: { value: 'fsa-B' } })
    expect(await screen.findByTestId('fsa-control-no-templates')).toBeInTheDocument()
  })

  it('names a failed template read', async () => {
    const { container } = await mountFsa({
      templates: [],
      controls: [],
      templatesReply: { ok: false, error: 'templates down', status: 500 },
    })
    await fireEvent.change(container.querySelector('#controlFsa') as HTMLSelectElement, { target: { value: 'fsa-A' } })
    expect(await screen.findByTestId('fsa-control-templates-error')).toHaveTextContent('templates down')
  })

  it('states how many assigned devices the FSA reaches', async () => {
    const { container } = await mountFsa({ templates: [template()], controls: [] })
    await fireEvent.change(container.querySelector('#controlFsa') as HTMLSelectElement, { target: { value: 'fsa-A' } })
    expect(await screen.findByTestId('fsa-control-reach')).toHaveTextContent('Reaches the 2 devices')
  })
})

describe('DerControl: a target switch while a send is in flight', () => {
  it('reloads the device table the send was made for, so switching back shows the new control', async () => {
    const state: { controls: unknown[] } = { controls: [] }
    vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
      const url = new URL(path, 'http://x')
      if (url.pathname === '/api/der/control-functions') return { ok: true, data: catalog }
      if (url.pathname === '/api/der/curves') return { ok: true, data: { curves: [] } }
      if (url.pathname === '/api/fsas') return { ok: true, data: { fsas } }
      if (url.pathname === '/api/devices/0/der-programs') return { ok: true, data: { device: '0', programs: [] } }
      if (url.pathname === '/api/devices/0/default-control') return { ok: false, error: 'none', status: 404 }
      if (url.pathname === '/api/der/controls') return { ok: true, data: { device: '0', controls: state.controls } }
      if (url.pathname === '/api/fsas/fsa-A/der-programs') return { ok: true, data: { templates: [] } }
      return { ok: false, error: 'unexpected path: ' + path, status: 404 }
    }) as never)
    let release!: (v: unknown) => void
    const post = vi.spyOn(api, 'postJSON').mockImplementation((() => new Promise((r) => (release = r))) as never)
    const devices: DashboardDevice[] = [
      { sfdi: '167261211635', lfdi: 'D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1', href: '/edev/0', enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: true, ders: [] },
    ]
    const { container } = render(DerControl, { devices })
    await screen.findByTestId('control-fn-fnFlag')
    await fireEvent.change(container.querySelector('#controlDevice') as HTMLSelectElement, { target: { value: '0' } })
    await fireEvent.click(tid('control-fn-fnFlag'))
    await fireEvent.input(container.querySelector('#controlDuration') as HTMLInputElement, { target: { value: '5' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))
    expect(post).toHaveBeenCalledTimes(1)

    await fireEvent.click(tid('control-target-fsa'))
    state.controls = [
      {
        mRID: 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB',
        href: '/edev/0/fsa/f/derp/p/derc/c',
        derProgramHref: '/edev/0/fsa/f/derp/p',
        derControlListHref: '/edev/0/fsa/f/derp/p/derc',
        type: 'fnFlag',
        description: '',
        derControlBase: {},
        functions: { fnFlag: true },
        creationTime: 1000,
        interval: { start: 5000, duration: 300 },
        eventStatus: { currentStatus: 0, status: 'scheduled', dateTime: 1000 },
        responses: { total: 0, byStatus: {} },
      },
    ]
    release({ ok: true, data: { kind: 'scheduled', fsa: 'f', program: 'p', programHref: '/edev/0/fsa/f/derp/p', primacy: 1, primacyReason: 'r' } })
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    await fireEvent.click(tid('control-target-device'))
    expect(await screen.findByTestId('der-control-row')).toHaveAttribute('data-mrid', 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB')
  })
})

describe('DerControl, FSA target: pending and active controls', () => {
  it('counts the live controls and can hide cancelled and ended ones', async () => {
    const live = fsaControl({ id: 'live', mRID: 'LIVE' })
    const cancelled = fsaControl({ id: 'gone', mRID: 'GONE', cancelled: true })
    const ended = fsaControl({ id: 'done', mRID: 'DONE', ended: true })
    const { container } = await mountFsa({ templates: [template()], controls: [cancelled, live, ended] })
    await pickProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('fsa-control-row')).toHaveLength(3))
    expect(tid('der-control-live-count')).toHaveTextContent('1 pending or active')

    await fireEvent.click(tid('der-control-live-only'))
    expect(screen.getAllByTestId('fsa-control-row').map((r) => r.getAttribute('data-mrid'))).toEqual(['LIVE'])
  })
})
