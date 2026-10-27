/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import DerControl from './DerControl.svelte'
import * as api from '../lib/api'
import type { DashboardDevice } from '../lib/dashboard'
import type { CatalogResponse, CurveListResponse } from '../lib/controlcatalog'
import { fmtTime } from '../lib/dercontrol'
import { useAppCss } from '../test-tokens'
import { takeProgramsRequest } from '../lib/programsRequest'
import type {
  DERControlCreated,
  DERControlListItem,
  DERControlListResponse,
  DERProgramListResponse,
  DeviceControlCreated,
} from '../lib/dercontrol'

const devices: DashboardDevice[] = [
  { sfdi: '167261211635', lfdi: 'D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1', href: '/edev/0', enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: true, ders: [] },
  { sfdi: '222222222222', lfdi: 'D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2', href: '/edev/1', enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: true, ders: [] },
]

const PROG_A = '/edev/0/fsa/0/derp/0'
const PROG_B = '/edev/0/fsa/0/derp/1'
const PROG_C = '/edev/1/fsa/0/derp/0'

// One catalog row of each kind. The names are arbitrary and every row carries
// a prose `value` that says something misleading: the form must ignore it.
const catalog: CatalogResponse = {
  functions: [
    { name: 'fnFlag', description: 'a flag', kind: 'boolean', value: 'integer 1 to 2' } as never,
    { name: 'fnLevel', description: 'a level', kind: 'integer', min: 0, max: 10000, value: 'boolean' } as never,
    {
      name: 'fnPair',
      description: 'an object',
      kind: 'object',
      fields: [
        { name: 'amount', kind: 'integer', required: true, min: 1, max: 1000 },
        { name: 'sign', kind: 'boolean', required: true },
        { name: 'scale', kind: 'integer', min: -9, max: 9 },
      ],
      value: 'curve',
    } as never,
    { name: 'fnCurve', description: 'a curve', kind: 'curve', curveType: 11, value: 'integer' } as never,
    { name: 'fnOther', description: 'another curve', kind: 'curve', curveType: 12 },
    { name: 'fnOdd', description: 'a kind the page lacks', kind: 'matrix' },
  ],
}

const curves: CurveListResponse = {
  curves: [
    { id: 'cv11', curveType: 11, description: 'volt-var one' },
    { id: 'cv12', curveType: 12, description: 'volt-watt one' },
    { id: 'cv11b', curveType: 11, description: 'volt-var two' },
  ],
}

function programsOf(device: string, rows: [string, string, number][]): DERProgramListResponse {
  return {
    device,
    programs: rows.map(([href, description, primacy]) => ({
      href,
      mRID: 'M' + description,
      description,
      primacy,
      derControlListHref: href + '/derc',
    })),
  }
}

const progs0 = programsOf('0', [
  [PROG_A, 'fixture program', 5],
  [PROG_B, 'device-only', 2],
])
const progs1 = programsOf('1', [[PROG_C, 'other device program', 9]])
const noControls: DERControlListResponse = { device: '0', controls: [] }

type Reply = unknown | Promise<unknown>

interface Mocks {
  catalogReply?: Reply
  curvesReply?: Reply
  programsFor?: (device: string) => Reply
  defaultFor?: (device: string) => Reply
  controlsFor?: (device: string) => Reply
}

// mockApi answers the GETs the panel issues. A reply is a full ApiResult (or
// a promise of one), so a test can return a failure or hold a response.
function mockApi(m: Mocks = {}) {
  const get = vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    const url = new URL(path, 'http://x')
    if (url.pathname === '/api/der/control-functions') return m.catalogReply ?? { ok: true, data: catalog }
    if (url.pathname === '/api/der/curves') return m.curvesReply ?? { ok: true, data: curves }
    const dflt = /\/api\/devices\/([^/]+)\/default-control/.exec(url.pathname)
    if (dflt) {
      const id = decodeURIComponent(dflt[1] ?? '')
      return m.defaultFor ? m.defaultFor(id) : { ok: false, error: 'device has no default control', status: 404 }
    }
    const dev = /\/api\/devices\/([^/]+)\/der-programs/.exec(url.pathname)
    if (dev) {
      const id = decodeURIComponent(dev[1] ?? '')
      if (m.programsFor) return m.programsFor(id)
      return { ok: true, data: id === '0' ? progs0 : progs1 }
    }
    if (url.pathname === '/api/der/controls') {
      const d = url.searchParams.get('device') ?? ''
      if (m.controlsFor) return m.controlsFor(d)
      return { ok: true, data: noControls }
    }
    return { ok: false, error: 'unexpected path: ' + path, status: 404 }
  }) as never)
  const post = vi.spyOn(api, 'postJSON')
  const controlGets = () => get.mock.calls.filter((c) => (c[0] as string).includes('/api/der/controls?')).length
  return { get, post, controlGets }
}

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

function control(over: Partial<DERControlCreated> = {}): DERControlCreated {
  return {
    mRID: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
    href: '/edev/0/fsa/0/derp/1/derc/c1',
    derProgramHref: PROG_B,
    derControlListHref: PROG_B + '/derc',
    type: 'combined',
    description: '',
    derControlBase: {},
    functions: { fnFlag: true },
    // creationTime, interval.start and eventStatus.dateTime all differ so
    // each shown time can be pinned to its own field.
    creationTime: 1000,
    interval: { start: 5000, duration: 300 },
    eventStatus: { currentStatus: 1, status: 'scheduled', dateTime: 3000 },
    supersedes: [],
    notificationAttempted: true,
    persisted: true,
    ...over,
  }
}

function scheduledReply(over: Partial<DERControlCreated> = {}, top: Partial<DeviceControlCreated> = {}): { ok: true; data: DeviceControlCreated } {
  return {
    ok: true,
    data: {
      kind: 'scheduled',
      fsa: 'dev-only',
      program: 'p1',
      programHref: PROG_B,
      primacy: 2,
      primacyReason: 'one above the device\'s other programs',
      control: control(over),
      ...top,
    },
  }
}

function defaultReply(): { ok: true; data: DeviceControlCreated } {
  return {
    ok: true,
    data: {
      kind: 'default',
      fsa: 'dev-only',
      program: 'p1',
      programHref: PROG_B,
      primacy: 2,
      primacyReason: 'one above the device\'s other programs',
      defaultControl: { mRID: 'DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD', type: 'combined', derControlBase: { someElement: 7 } },
    },
  }
}

function item(mRID: string, status: string, start: number, duration: number, over: Partial<DERControlListItem> = {}): DERControlListItem {
  return {
    ...control({ mRID, derProgramHref: PROG_A, functions: { fnLevel: 4550 }, type: 'fnLevel', interval: { start, duration }, eventStatus: { currentStatus: 1, status, dateTime: 0 } }),
    responses: { total: 2, byStatus: { '1': 2 } },
    delivery: null,
    ...over,
  } as DERControlListItem
}

const sel = (c: HTMLElement, id: string) => c.querySelector(id) as HTMLSelectElement
const inp = (c: HTMLElement, id: string) => c.querySelector(id) as HTMLInputElement
const tid = (id: string) => screen.getByTestId(id)
const result = (c: HTMLElement) => c.querySelector('#controlResult') as HTMLElement

async function mount(m: Mocks = {}) {
  const mocks = mockApi(m)
  const view = render(DerControl, { devices })
  // The catalog arrives after mount; its rows are the form.
  await screen.findByTestId('control-fn-fnFlag')
  return { ...mocks, container: view.container }
}

async function pickDevice(c: HTMLElement, id = '0') {
  await fireEvent.change(sel(c, '#controlDevice'), { target: { value: id } })
  await waitFor(() => expect(screen.queryAllByTestId('der-control-program-row').length).toBeGreaterThan(0))
}

async function tick(name: string) {
  await fireEvent.click(tid(`control-fn-${name}`))
}

async function fillFlagSchedule(c: HTMLElement) {
  await pickDevice(c)
  await tick('fnFlag')
  await fireEvent.click(tid('control-fv-fnFlag'))
  await fireEvent.input(inp(c, '#controlDuration'), { target: { value: '5' } })
}

async function sendAndConfirm() {
  await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))
}

describe('DerControl: the form is built from the catalog by kind', () => {
  it('shows one row per catalog entry and its value field only once ticked', async () => {
    await mount()
    expect(screen.getAllByTestId(/^control-function-/)).toHaveLength(6)
    expect(screen.queryByTestId('control-fv-fnFlag')).toBeNull()
    await tick('fnFlag')
    expect(tid('control-fv-fnFlag')).toHaveAttribute('type', 'checkbox')
  })

  it('renders a boolean, an integer, an object and a curve field from kind, min, max, fields and curveType', async () => {
    await mount()
    for (const n of ['fnFlag', 'fnLevel', 'fnPair', 'fnCurve']) await tick(n)

    expect(tid('control-fv-fnFlag')).toHaveAttribute('type', 'checkbox')

    const level = tid('control-fv-fnLevel')
    expect(level).toHaveAttribute('type', 'text')
    expect(level).toHaveAttribute('placeholder', '0 to 10000')

    expect(tid('control-fv-fnPair-amount')).toHaveAttribute('placeholder', '1 to 1000')
    expect(tid('control-fv-fnPair-sign')).toHaveAttribute('type', 'checkbox')
    expect(tid('control-fv-fnPair-scale')).toHaveAttribute('placeholder', '-9 to 9 (optional)')

    const options = Array.from((tid('control-fv-fnCurve') as HTMLSelectElement).options).map((o) => o.value)
    expect(options).toEqual(['', 'cv11', 'cv11b'])
  })

  it('a row of a kind the page lacks is shown but cannot be ticked', async () => {
    await mount()
    const row = tid('control-function-fnOdd')
    expect(row).toHaveTextContent('Not supported on this page.')
    expect(tid('control-fn-fnOdd')).toBeDisabled()
  })

  it('an empty curve list says no curve of the type is stored', async () => {
    await mount({ curvesReply: { ok: true, data: { curves: [] } } })
    await tick('fnCurve')
    expect((tid('control-fv-fnCurve') as HTMLSelectElement).options[0]?.textContent).toBe('(no stored curve of this type)')
  })

  it('names a failed curve read beside the curve field', async () => {
    await mount({ curvesReply: { ok: false, error: 'curve read failed', status: 500 } })
    await tick('fnCurve')
    expect(tid('control-curves-error')).toHaveTextContent('curve read failed')
  })

  it('names a failed catalog read', async () => {
    mockApi({ catalogReply: { ok: false, error: 'catalog down', status: 500 } })
    render(DerControl, { devices })
    expect(await screen.findByTestId('control-catalog-error')).toHaveTextContent('catalog down')
  })
})

describe('DerControl: a scheduled control with several functions', () => {
  it('Send posts nothing and names the values; Confirm posts one request, and a rapid second click adds none', async () => {
    const { post, container } = await mount()
    const gate = deferred<unknown>()
    post.mockImplementation((() => gate.promise) as never)
    await pickDevice(container)
    for (const n of ['fnFlag', 'fnLevel', 'fnPair', 'fnCurve']) await tick(n)
    await fireEvent.click(tid('control-fv-fnFlag'))
    await fireEvent.input(tid('control-fv-fnLevel'), { target: { value: '4550' } })
    await fireEvent.input(tid('control-fv-fnPair-amount'), { target: { value: '950' } })
    await fireEvent.click(tid('control-fv-fnPair-sign'))
    await fireEvent.change(tid('control-fv-fnCurve'), { target: { value: 'cv11b' } })
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '5' } })

    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    const summary = tid('der-control-confirm')
    expect(post).not.toHaveBeenCalled()
    expect(summary).toHaveTextContent('device 167261211635')
    expect(summary).toHaveTextContent('fnFlag=true, fnLevel=4550, fnPair={"amount":950,"sign":true}, fnCurve="cv11b"')

    const confirm = within(summary).getByRole('button', { name: 'Confirm' })
    await fireEvent.click(confirm)
    await fireEvent.click(confirm)
    expect(post).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledWith('/api/devices/0/controls', {
      kind: 'scheduled',
      functions: { fnFlag: true, fnLevel: 4550, fnPair: { amount: 950, sign: true }, fnCurve: 'cv11b' },
      durationSeconds: 300,
    })
    gate.resolve(scheduledReply())
  })

  it('posts a chosen start time and description', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue(scheduledReply())
    await fillFlagSchedule(container)
    await fireEvent.click(inp(container, '#controlStartNow'))
    await fireEvent.input(inp(container, '#controlStartAt'), { target: { value: '2030-01-02T03:04:05' } })
    await fireEvent.input(inp(container, '#controlDescription'), { target: { value: 'peak' } })
    await sendAndConfirm()
    const body = post.mock.calls[0]?.[1] as Record<string, unknown>
    expect(body.startTime).toBe(Math.floor(Date.parse('2030-01-02T03:04:05') / 1000))
    expect(body.description).toBe('peak')
  })

  it('a bad value or an empty selection is refused on Send with nothing posted', async () => {
    const { post, container } = await mount()
    await pickDevice(container)
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '5' } })

    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(result(container)).toHaveTextContent('Tick at least one function.')

    await tick('fnLevel')
    await fireEvent.input(tid('control-fv-fnLevel'), { target: { value: '10001' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(result(container)).toHaveTextContent('fnLevel must be a whole number from 0 to 10000.')
    expect(screen.queryByTestId('der-control-confirm')).toBeNull()
    expect(post).not.toHaveBeenCalled()
  })

  it('refuses a send with no device picked', async () => {
    const { post, container } = await mount()
    await tick('fnFlag')
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '5' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(result(container)).toHaveTextContent('Pick a device first.')
    expect(post).not.toHaveBeenCalled()
  })

  it('refuses a missing duration', async () => {
    const { post, container } = await mount()
    await pickDevice(container)
    await tick('fnFlag')
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(result(container)).toHaveTextContent('Duration must be a positive number of minutes.')
    expect(post).not.toHaveBeenCalled()
  })
})

describe('DerControl: a default control', () => {
  it('hides the schedule fields, posts kind default with the functions only, and shows the default first', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue(defaultReply())
    await pickDevice(container)
    await fireEvent.click(tid('control-mode-default'))
    expect(container.querySelector('#controlDuration')).toBeNull()
    expect(container.querySelector('#controlStartNow')).toBeNull()

    await tick('fnLevel')
    await fireEvent.input(tid('control-fv-fnLevel'), { target: { value: '6000' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(tid('der-control-confirm')).toHaveTextContent('in force now')
    expect(tid('der-control-confirm')).toHaveTextContent('no end')
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/devices/0/controls', { kind: 'default', functions: { fnLevel: 6000 } })
    expect(result(container)).toHaveTextContent('Stored the default control DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD')

    const row = await screen.findByTestId('der-control-default-row')
    expect(row).toHaveTextContent('someElement=7')
    expect(row).toHaveTextContent(`${PROG_B} (2)`)
    // The default sits above every scheduled row.
    const rows = Array.from(container.querySelectorAll('#derControlsTable tbody tr'))
    expect(rows[0]).toBe(row)
  })

  it('shows no default row for another device', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue(defaultReply())
    await pickDevice(container)
    await fireEvent.click(tid('control-mode-default'))
    await tick('fnFlag')
    await sendAndConfirm()
    await screen.findByTestId('der-control-default-row')
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(screen.queryByTestId('der-control-default-row')).toBeNull())
  })
})

describe('DerControl: the stored default is read from the server', () => {
  const stored = { ok: true, data: { mRID: 'SSSS', type: 'combined', derControlBase: { someElement: 4000 } } }

  it('shows a stored default on load, first in the table, with no program claimed', async () => {
    const { container, get } = await mount({ defaultFor: () => stored })
    await pickDevice(container)
    const row = await screen.findByTestId('der-control-default-row')
    expect(row).toHaveTextContent('someElement=4000')
    expect(row.querySelectorAll('td')[2]?.textContent?.trim()).toBe('-')
    expect(get.mock.calls.map((c) => c[0])).toContain('/api/devices/0/default-control')
    expect(container.querySelector('#derControlsTable tbody tr')).toBe(row)
  })

  it('a 404 shows no default and no error', async () => {
    const { container } = await mount()
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-program-row').length).toBeGreaterThan(0))
    expect(screen.queryByTestId('der-control-default-row')).toBeNull()
    expect(screen.queryByTestId('der-control-default-error')).toBeNull()
  })

  it('any other failure is shown as an error, not as no default', async () => {
    const { container } = await mount({ defaultFor: () => ({ ok: false, error: 'store down', status: 500 }) })
    await pickDevice(container)
    expect(await screen.findByTestId('der-control-default-error')).toHaveTextContent('store down')
    expect(screen.queryByTestId('der-control-default-row')).toBeNull()
  })

  it('a device switch discards a stale reply, even when the same device is picked again', async () => {
    const slow = deferred<unknown>()
    let calls = 0
    const { container } = await mount({
      defaultFor: () => (++calls === 1 ? slow.promise : { ok: false, error: 'none', status: 404 }),
    })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await waitFor(() => expect(calls).toBe(3))
    await waitFor(() => expect(screen.getAllByTestId('der-control-program-row').length).toBeGreaterThan(0))
    slow.resolve(stored)
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByTestId('der-control-default-row')).toBeNull()
  })

  it('a default send replaces what was read and names its program', async () => {
    const { post, container } = await mount({ defaultFor: () => stored })
    post.mockResolvedValue(defaultReply())
    await pickDevice(container)
    await screen.findByTestId('der-control-default-row')
    await fireEvent.click(tid('control-mode-default'))
    await tick('fnFlag')
    await sendAndConfirm()
    await waitFor(() => expect(tid('der-control-default-row')).toHaveTextContent('someElement=7'))
    expect(tid('der-control-default-row')).toHaveTextContent(PROG_B)
  })
})

describe('DerControl: a curve pick', () => {
  it('lists only the stored curves of the row\'s type and posts the id picked', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue(scheduledReply())
    await pickDevice(container)
    await tick('fnOther')
    const options = Array.from((tid('control-fv-fnOther') as HTMLSelectElement).options).map((o) => o.value)
    expect(options).toEqual(['', 'cv12'])
    await fireEvent.change(tid('control-fv-fnOther'), { target: { value: 'cv12' } })
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '1' } })
    await sendAndConfirm()
    expect(post).toHaveBeenCalledWith('/api/devices/0/controls', { kind: 'scheduled', functions: { fnOther: 'cv12' }, durationSeconds: 60 })
  })

  it('refuses Send while no curve is picked', async () => {
    const { post, container } = await mount()
    await pickDevice(container)
    await tick('fnCurve')
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '1' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(result(container)).toHaveTextContent('fnCurve: pick a curve.')
    expect(post).not.toHaveBeenCalled()
  })
})

describe('DerControl: result area and primacy', () => {
  it('shows the stored time, mRID, status, a nominal read estimate and the primacy the server chose', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue(scheduledReply())
    await fillFlagSchedule(container)
    await sendAndConfirm()
    await waitFor(() => expect(result(container).textContent).toContain('Stored AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'))
    const text = result(container).textContent ?? ''
    expect(text).toContain(`at ${fmtTime(1000)}`)
    expect(text).toContain(`starts ${fmtTime(5000)}`)
    expect(text).toContain(`as of ${fmtTime(3000)}`)
    expect(text).toContain(`by ${fmtTime(1000 + 900)}`)
    expect(text).not.toMatch(/\bsent\b|delivered/i)
    expect(tid('der-control-primacy')).toHaveTextContent("Primacy 2: one above the device's other programs")
  })

  it('names how many earlier controls the new one superseded, and the restart note when not persisted', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue(scheduledReply({ supersedes: ['X'], persisted: false }))
    await fillFlagSchedule(container)
    await sendAndConfirm()
    await waitFor(() => expect(result(container)).toHaveTextContent('Superseded 1 earlier control.'))
    expect(result(container)).toHaveTextContent('A server restart forgets this control.')
  })

  it('a device whose program holds primacy 0 gets the server\'s refusal as written, no primacy line, and a reloaded table', async () => {
    const { post, container, controlGets } = await mount()
    post.mockResolvedValue({ ok: false, error: 'no primacy outranks program /edev/0/fsa/0/derp/p1, which holds primacy 0; change or remove that program first', status: 409, body: { code: 'primacy_cannot_outrank' } })
    await fillFlagSchedule(container)
    const before = controlGets()
    await sendAndConfirm()
    await waitFor(() => expect(result(container)).toHaveTextContent('Error: no primacy outranks program /edev/0/fsa/0/derp/p1, which holds primacy 0; change or remove that program first'))
    expect(screen.queryByTestId('der-control-primacy')).toBeNull()
    await waitFor(() => expect(controlGets()).toBeGreaterThan(before))
  })

  it('a controlKept 500 says the control was kept and may be live, naming its mRID', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue({
      ok: false,
      status: 500,
      error: 'internal error, see server log',
      body: { error: 'x', mRID: 'KEPT', href: '/edev/0/fsa/0/derp/1/derc/KEPT', controlKept: true },
    })
    await fillFlagSchedule(container)
    await sendAndConfirm()
    await waitFor(() => expect(result(container)).toHaveTextContent('The control was kept and may be live: KEPT'))
    expect(result(container)).toHaveTextContent('Cancel it from the table if it is not wanted.')
  })
})

describe('DerControl: a pending confirmation follows the device', () => {
  it('changing the device after Send drops the confirmation; nothing posts', async () => {
    const { post, container } = await mount()
    await fillFlagSchedule(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(screen.queryByTestId('der-control-confirm')).not.toBeNull()
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(screen.queryByTestId('der-control-confirm')).toBeNull())
    expect(post).not.toHaveBeenCalled()
  })

  it('a create reply held across a device change is named with the device it was for', async () => {
    const { post, container } = await mount()
    const gate = deferred<unknown>()
    post.mockImplementation((() => gate.promise) as never)
    await fillFlagSchedule(container)
    await sendAndConfirm()
    expect(sel(container, '#controlDevice')).toBeDisabled()
    gate.resolve(scheduledReply())
    await waitFor(() => expect(result(container)).toHaveTextContent('Stored AAAA'))
    expect(result(container).textContent).not.toContain('For device')
  })
})

describe('DerControl: programs, primacy and the controls table', () => {
  const now = Math.floor(Date.now() / 1000)
  const rows = {
    device: '0',
    controls: [
      item('SCHED', 'scheduled', now + 3600, 600),
      item('ACTIVE', 'active', now - 10, 3600, { derProgramHref: PROG_B }),
      item('ENDED', 'active', now - 7200, 60, { ended: true }),
      item('CANCELLED', 'cancelled', now - 10, 3600),
      item('SUPERSEDED', 'superseded', now - 10, 3600),
    ],
  } satisfies DERControlListResponse
  const reply = () => ({ ok: true, data: rows, serverTime: now * 1000 })

  it('lists the device\'s programs with primacy and control count', async () => {
    const { container } = await mount({ controlsFor: reply })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    const programRows = screen.getAllByTestId('der-control-program-row').map((r) => Array.from(r.querySelectorAll('td')).map((td) => td.textContent?.trim()))
    expect(programRows).toEqual([
      ['fixture program', '5', '4'],
      ['device-only', '2', '1'],
    ])
  })

  it('lists type, functions, program with primacy, times, status and device-reported responses', async () => {
    const { container } = await mount({ controlsFor: reply })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    const row = container.querySelector('[data-mrid="SCHED"]') as HTMLElement
    const cells = Array.from(row.querySelectorAll('td')).map((td) => td.textContent?.trim())
    expect(cells[0]).toBe('fnLevel')
    expect(cells[1]).toBe('fnLevel=4550')
    expect(cells[2]).toBe(`${PROG_A} (5)`)
    expect(cells[3]).toBe(fmtTime(now + 3600))
    expect(cells[4]).toBe(fmtTime(now + 3600 + 600))
    expect(cells[5]).toBe('scheduled')
    expect(cells[6]).toContain('2 reported')
    expect(container.querySelector('#derControlsTable')).toHaveTextContent('reported by device, not verified by server')
    expect(container.querySelector('[data-mrid="ENDED"] [data-testid="der-control-status"]')).toHaveTextContent('active (ended)')
  })

  it('the program pick narrows the table, and (all programs) restores it', async () => {
    const { container } = await mount({ controlsFor: reply })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })
    expect(screen.getAllByTestId('der-control-row')).toHaveLength(1)
    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: '' } })
    expect(screen.getAllByTestId('der-control-row')).toHaveLength(5)
  })

  it('counts the pending and active controls, and can hide the rest', async () => {
    const { container } = await mount({ controlsFor: reply })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    expect(tid('der-control-live-count')).toHaveTextContent('2 pending or active')

    await fireEvent.click(tid('der-control-live-only'))
    const mrids = screen.getAllByTestId('der-control-row').map((r) => r.getAttribute('data-mrid'))
    expect(mrids).toEqual(['SCHED', 'ACTIVE'])

    await fireEvent.click(tid('der-control-live-only'))
    expect(screen.getAllByTestId('der-control-row')).toHaveLength(5)
  })

  it('the filter keeps exactly the rows that offer Cancel', async () => {
    const { container } = await mount({ controlsFor: reply })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    const cancellable = screen
      .getAllByTestId('der-control-row')
      .filter((r) => within(r).queryByRole('button', { name: 'Cancel' }) !== null)
      .map((r) => r.getAttribute('data-mrid'))
    expect(cancellable).toEqual(['SCHED', 'ACTIVE'])
    await fireEvent.click(tid('der-control-live-only'))
    expect(screen.getAllByTestId('der-control-row').map((r) => r.getAttribute('data-mrid'))).toEqual(cancellable)
  })

  it('says none are pending or active when the filter hides only finished controls', async () => {
    const finished = { device: '0', controls: [item('CANCELLED', 'cancelled', now - 10, 3600)] }
    const { container } = await mount({ controlsFor: () => ({ ok: true, data: finished, serverTime: now * 1000 }) })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    await fireEvent.click(tid('der-control-live-only'))
    expect(tid('der-control-empty')).toHaveTextContent('No pending or active controls for this selection')
    expect(tid('der-control-live-count')).toHaveTextContent('0 pending or active')
  })

  it('shows no live count while the list loads or after a failed read', async () => {
    const slow = deferred<unknown>()
    const { container } = await mount({ controlsFor: () => slow.promise })
    await pickDevice(container)
    expect(screen.queryByTestId('der-control-live-count')).toBeNull()
    slow.resolve({ ok: false, error: 'list failed', status: 500 })
    await waitFor(() => expect(tid('der-control-controls-error')).toBeInTheDocument())
    expect(screen.queryByTestId('der-control-live-count')).toBeNull()
  })

  it('sends a second control from the same form without retyping it', async () => {
    const { post, container } = await mount()
    post.mockResolvedValue(scheduledReply())
    await fillFlagSchedule(container)
    await sendAndConfirm()
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    await fireEvent.click(inp(container, '#controlStartNow'))
    await fireEvent.input(inp(container, '#controlStartAt'), { target: { value: '2030-01-02T03:04:05' } })
    await sendAndConfirm()
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    const second = post.mock.calls[1]?.[1] as Record<string, unknown>
    expect(second.functions).toEqual({ fnFlag: true })
    expect(second.durationSeconds).toBe(300)
    expect(second.startTime).toBe(Math.floor(Date.parse('2030-01-02T03:04:05') / 1000))
  })

  it('offers Cancel on Scheduled and on Active-not-ended rows only', async () => {
    const { container } = await mount({ controlsFor: reply })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    for (const present of ['SCHED', 'ACTIVE']) expect(screen.queryByTestId(`der-control-cancel-${present}`)).not.toBeNull()
    for (const absent of ['ENDED', 'CANCELLED', 'SUPERSEDED']) expect(screen.queryByTestId(`der-control-cancel-${absent}`)).toBeNull()
  })

  it('cancels only after a second confirmation, posts the typed reason, then reloads the table', async () => {
    const { post, controlGets, container } = await mount({ controlsFor: reply })
    post.mockResolvedValue({ ok: true, data: {} })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    const before = controlGets()
    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    expect(post).not.toHaveBeenCalled()
    await fireEvent.input(screen.getByPlaceholderText('reason (optional)'), { target: { value: 'maintenance' } })
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-SCHED'))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/der/controls/SCHED/cancel', { reason: 'maintenance' })
    await waitFor(() => expect(controlGets()).toBeGreaterThan(before))
  })

  it('Back abandons the cancellation without posting; with no reason the body is empty', async () => {
    const { post, container } = await mount({ controlsFor: reply })
    post.mockResolvedValue({ ok: true, data: {} })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    await fireEvent.click(screen.getByTestId('der-control-cancel-ACTIVE'))
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(post).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByTestId('der-control-cancel-ACTIVE'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-ACTIVE'))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/der/controls/ACTIVE/cancel', {}))
  })

  it('shows a cancel refusal, and a cancel 500 naming a kept control says it still exists', async () => {
    const { post, container } = await mount({ controlsFor: reply })
    post.mockResolvedValue({ ok: false, error: 'control already ended', status: 409 })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-SCHED'))
    await waitFor(() => expect(screen.getByTestId('der-control-cancel-result')).toHaveTextContent('Cancel failed: control already ended'))

    post.mockResolvedValue({
      ok: false,
      status: 500,
      error: 'cancellation may be recorded',
      body: { error: 'x', mRID: 'SCHED', href: '/h', controlKept: true },
    })
    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-SCHED'))
    await waitFor(() => expect(screen.getByTestId('der-control-cancel-result')).toHaveTextContent('still exists'))
    expect(screen.getByTestId('der-control-cancel-result').textContent).not.toContain('Cancel failed')
  })
})

describe('DerControl: failed reads are errors, not empty results', () => {
  it('a failed programs GET shows the server error, not the no-programs note', async () => {
    const { container } = await mount({ programsFor: () => ({ ok: false, error: 'store down', status: 500 }) })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    expect(await screen.findByTestId('der-control-programs-error')).toHaveTextContent('store down')
    expect(screen.queryByTestId('der-control-no-programs')).toBeNull()
  })

  it('a failed controls GET shows the server error, not an empty table; the next success clears it', async () => {
    let fail = true
    const { container } = await mount({ controlsFor: () => (fail ? { ok: false, error: 'list failed', status: 500 } : { ok: true, data: noControls }) })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    expect(await screen.findByTestId('der-control-controls-error')).toHaveTextContent('list failed')
    fail = false
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(screen.queryByTestId('der-control-controls-error')).toBeNull())
    expect(container.querySelector('#derControlsTable')).toHaveTextContent('No admin-issued controls')
  })
})

describe('DerControl: stale-response guards', () => {
  it('a slow programs reply for the old device cannot overwrite the new device', async () => {
    const slow = deferred<unknown>()
    const { container } = await mount({ programsFor: (id) => (id === '0' ? slow.promise : { ok: true, data: progs1 }) })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(screen.getAllByTestId('der-control-program-row')).toHaveLength(1))
    slow.resolve({ ok: true, data: progs0 })
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.getAllByTestId('der-control-program-row')).toHaveLength(1)
    expect(screen.getByTestId('der-control-program-row')).toHaveTextContent('other device program')
  })

  it('a slow failing programs reply for the old device raises no error for the new one', async () => {
    const slow = deferred<unknown>()
    const { container } = await mount({ programsFor: (id) => (id === '0' ? slow.promise : { ok: true, data: progs1 }) })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(screen.getAllByTestId('der-control-program-row')).toHaveLength(1))
    slow.resolve({ ok: false, error: 'late failure', status: 500 })
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByTestId('der-control-programs-error')).toBeNull()
  })

  it('a slow controls reply for the old device cannot overwrite the new device\'s table', async () => {
    const slow = deferred<unknown>()
    const start = Math.floor(Date.now() / 1000) + 60
    const fast = { device: '1', controls: [item('FASTROW', 'scheduled', start, 60)] }
    const stale = { device: '0', controls: [item('STALEROW', 'scheduled', start, 60)] }
    const { container } = await mount({ controlsFor: (d) => (d === '0' ? slow.promise : { ok: true, data: fast }) })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(container.querySelector('[data-mrid="FASTROW"]')).toBeTruthy())
    slow.resolve({ ok: true, data: stale })
    await new Promise((r) => setTimeout(r, 20))
    expect(container.querySelector('[data-mrid="STALEROW"]')).toBeNull()
  })
})

describe('DerControl: no DER programs', () => {
  const none = { programsFor: () => ({ ok: true, data: { device: '0', programs: [] } }) }

  it('says a control creates a device-only program, and links the FSAs and Devices tabs', async () => {
    const { container } = await mount(none)
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    const note = await screen.findByTestId('der-control-no-programs')
    expect(note).toHaveTextContent('no DER programs')
    expect(note).toHaveTextContent('creates one for this device alone')
    expect(within(note).getByRole('link', { name: 'FSAs tab' })).toHaveAttribute('href', '/ui/fsas')
    expect(within(note).getByRole('link', { name: 'Devices tab' })).toHaveAttribute('href', '/ui/devices')
    expect(screen.queryByTestId('der-control-programs-error')).toBeNull()
  })

  it('asks the Devices tab to open the chosen device Programs drawer when its link is clicked', async () => {
    const { container } = await mount(none)
    window.history.pushState({}, '', '/ui/control')
    takeProgramsRequest()
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    const note = await screen.findByTestId('der-control-no-programs')
    await fireEvent.click(within(note).getByRole('link', { name: 'Devices tab' }))
    expect(window.location.pathname).toBe('/ui/devices')
    expect(takeProgramsRequest()).toBe('0')
  })

  it('leaves no request when the Devices link is modifier-clicked', async () => {
    const { container } = await mount(none)
    window.history.pushState({}, '', '/ui/control')
    takeProgramsRequest()
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    const note = await screen.findByTestId('der-control-no-programs')
    await fireEvent.click(within(note).getByRole('link', { name: 'Devices tab' }), { ctrlKey: true })
    expect(takeProgramsRequest()).toBe('')
  })
})

describe('DerControl: "now" for Cancel comes from the server clock', () => {
  const browserNow = Math.floor(Date.now() / 1000)
  // Ends 50 s after the server's clock but 100 s before the browser's.
  const serverNow = browserNow - 150
  const live = { device: '0', controls: [item('LIVE', 'active', serverNow - 10, 60)] }

  it('keeps Cancel on an Active row the browser clock alone would call ended', async () => {
    const { container } = await mount({ controlsFor: () => ({ ok: true, data: live, serverTime: serverNow * 1000 }) })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    expect(screen.getByTestId('der-control-cancel-LIVE')).toBeInTheDocument()
  })

  it('hides Cancel once the server clock is past the end, refreshing that clock on each reload', async () => {
    let serverMs = (serverNow - 10) * 1000
    const { post, container } = await mount({ controlsFor: () => ({ ok: true, data: live, serverTime: serverMs }) })
    post.mockResolvedValue({ ok: false, error: 'refused', status: 400 })
    await fillFlagSchedule(container)
    await waitFor(() => expect(screen.getByTestId('der-control-cancel-LIVE')).toBeInTheDocument())
    serverMs = (serverNow + 500) * 1000
    await sendAndConfirm()
    await waitFor(() => expect(screen.queryByTestId('der-control-cancel-LIVE')).toBeNull())
  })

  it('with no server time the Active row keeps Cancel and the server decides', async () => {
    const ended = { device: '0', controls: [item('OLD', 'active', browserNow - 7200, 60)] }
    const { container } = await mount({ controlsFor: () => ({ ok: true, data: ended }) })
    await pickDevice(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    expect(screen.getByTestId('der-control-cancel-OLD')).toBeInTheDocument()
  })
})

// jsdom does no layout, so the stylesheet is injected and read back through
// getComputedStyle: what is asserted is the rule the browser would apply.
describe('DerControl: the function rows lay out in three columns', () => {
  useAppCss()

  it('puts every catalog row in one three-column grid, in catalog order', async () => {
    await mount()
    const grid = tid('control-functions')
    expect(getComputedStyle(grid).display).toBe('grid')
    expect(getComputedStyle(grid).gridTemplateColumns).toMatch(/^repeat\(3,/)
    const names = Array.from(grid.querySelectorAll('[data-testid^="control-function-"]')).map((r) => r.getAttribute('data-testid'))
    expect(names).toEqual(['fnFlag', 'fnLevel', 'fnPair', 'fnCurve', 'fnOther', 'fnOdd'].map((n) => `control-function-${n}`))
  })

  it('drops to one column on a narrow window', () => {
    const css = readFileSync('src/app.css', 'utf8')
    const narrow = /@media\s*\(max-width:\s*800px\)\s*\{\s*\.control-functions\s*\{\s*grid-template-columns:\s*1fr;/.exec(css)
    expect(narrow).not.toBeNull()
  })
})

describe('DerControl: the start picker defaults to now', () => {
  afterEach(() => vi.useRealTimers())

  it('opens filled with the local date and time to the second, taken when Start now is unchecked', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2031, 4, 6, 7, 8, 9))
    const { container } = await mount()
    expect(container.querySelector('#controlStartAt')).toBeNull()

    vi.setSystemTime(new Date(2031, 4, 6, 7, 9, 10))
    await fireEvent.click(inp(container, '#controlStartNow'))
    // jsdom's datetime-local sanitizer appends .000; a browser keeps the seconds.
    expect(inp(container, '#controlStartAt').value).toMatch(/^2031-05-06T07:09:10(\.000)?$/)

    await fireEvent.click(inp(container, '#controlStartNow'))
    vi.setSystemTime(new Date(2031, 11, 31, 23, 59, 58))
    await fireEvent.click(inp(container, '#controlStartNow'))
    expect(inp(container, '#controlStartAt').value).toMatch(/^2031-12-31T23:59:58(\.000)?$/)
  })

  it('keeps a future start the user typed and replaces one that has passed', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2031, 4, 6, 7, 8, 9))
    const { container } = await mount()
    await fireEvent.click(inp(container, '#controlStartNow'))
    await fireEvent.input(inp(container, '#controlStartAt'), { target: { value: '2032-01-02T03:04:05' } })
    await fireEvent.click(inp(container, '#controlStartNow'))
    await fireEvent.click(inp(container, '#controlStartNow'))
    expect(inp(container, '#controlStartAt').value).toMatch(/^2032-01-02T03:04:05(\.000)?$/)

    await fireEvent.input(inp(container, '#controlStartAt'), { target: { value: '2030-01-02T03:04:05' } })
    await fireEvent.click(inp(container, '#controlStartNow'))
    await fireEvent.click(inp(container, '#controlStartNow'))
    expect(inp(container, '#controlStartAt').value).toMatch(/^2031-05-06T07:08:09(\.000)?$/)
  })

  it('sends the defaulted start when the form is submitted untouched', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2031, 4, 6, 7, 8, 9))
    const { post, container } = await mount()
    post.mockResolvedValue(scheduledReply())
    await fillFlagSchedule(container)
    await fireEvent.click(inp(container, '#controlStartNow'))
    await sendAndConfirm()
    const body = post.mock.calls[0]?.[1] as Record<string, unknown>
    expect(body.startTime).toBe(Math.floor(new Date(2031, 4, 6, 7, 8, 9).getTime() / 1000))
  })
})
