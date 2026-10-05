import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import DerControl from './DerControl.svelte'
import * as api from '../lib/api'
import type { DashboardDevice } from '../lib/dashboard'
import { fmtTime } from '../lib/dercontrol'
import type {
  DERControlCreated,
  DERControlListItem,
  DERControlListResponse,
  DERProgramListResponse,
} from '../lib/dercontrol'

const devices: DashboardDevice[] = [
  { sfdi: '167261211635', lfdi: 'D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1', href: '/edev/0', enabled: true, lastRequest: null, comms: 'not_seen' },
  { sfdi: '222222222222', lfdi: 'D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2', href: '/edev/1', enabled: true, lastRequest: null, comms: 'not_seen' },
]

const PROG_A = '/edev/0/fsa/0/derp/0'
const PROG_B = '/edev/0/fsa/0/derp/1'
const PROG_C = '/edev/1/fsa/0/derp/0'

function programsOf(device: string, hrefs: [string, string][]): DERProgramListResponse {
  return {
    device,
    programs: hrefs.map(([href, description]) => ({
      href,
      mRID: 'M' + description,
      description,
      primacy: 0,
      derControlListHref: href + '/derc',
    })),
  }
}

const progs0 = programsOf('0', [
  [PROG_A, 'fixture program'],
  [PROG_B, 'second program'],
])
const progs1 = programsOf('1', [[PROG_C, 'other device program']])
const noControls: DERControlListResponse = { device: '0', controls: [] }

type Reply = unknown | Promise<unknown>

interface Mocks {
  programsFor?: (device: string) => Reply
  controlsFor?: (device: string, program: string) => Reply
}

// mockApi answers the two GETs the panel issues. A reply is a full ApiResult
// (or a promise of one), so a test can return a failure or hold a response.
function mockApi(m: Mocks = {}) {
  const get = vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    const url = new URL(path, 'http://x')
    const dev = /\/api\/devices\/([^/]+)\/der-programs/.exec(url.pathname)
    if (dev) {
      const id = decodeURIComponent(dev[1] ?? '')
      if (m.programsFor) return m.programsFor(id)
      return { ok: true, data: id === '0' ? progs0 : progs1 }
    }
    if (url.pathname === '/api/der/controls') {
      const d = url.searchParams.get('device') ?? ''
      const p = url.searchParams.get('derProgramHref') ?? ''
      if (m.controlsFor) return m.controlsFor(d, p)
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

function created(over: Partial<DERControlCreated> = {}): { ok: true; data: DERControlCreated } {
  return {
    ok: true,
    data: {
      mRID: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
      href: '/edev/0/fsa/0/derp/0/derc/c1',
      derProgramHref: PROG_A,
      derControlListHref: PROG_A + '/derc',
      type: 'connect',
      description: '',
      derControlBase: { opModConnect: true },
      // creationTime, interval.start and eventStatus.dateTime all differ so
      // each shown time can be pinned to its own field.
      creationTime: 1000,
      interval: { start: 5000, duration: 300 },
      eventStatus: { currentStatus: 1, status: 'scheduled', dateTime: 3000 },
      supersedes: [],
      notificationAttempted: true,
      persisted: true,
      ...over,
    },
  }
}

function item(mRID: string, status: string, start: number, duration: number, over: Partial<DERControlListItem> = {}): DERControlListItem {
  return {
    mRID,
    href: `/edev/0/fsa/0/derp/0/derc/${mRID}`,
    derProgramHref: PROG_A,
    derControlListHref: PROG_A + '/derc',
    type: 'maxLimW',
    description: '',
    derControlBase: { opModMaxLimW: 4550 },
    creationTime: 0,
    interval: { start, duration },
    eventStatus: { currentStatus: 1, status, dateTime: 0 },
    responses: { total: 2, byStatus: { '1': 2 } },
    ...over,
  }
}

const sel = (c: HTMLElement, id: string) => c.querySelector(id) as HTMLSelectElement
const inp = (c: HTMLElement, id: string) => c.querySelector(id) as HTMLInputElement

async function pickDevice(c: HTMLElement, id: string, optionCount: number) {
  await fireEvent.change(sel(c, '#controlDevice'), { target: { value: id } })
  await waitFor(() => expect(sel(c, '#controlProgram').options).toHaveLength(optionCount + 1))
}

async function pickDeviceAndProgram(c: HTMLElement, program = PROG_A) {
  await pickDevice(c, '0', 2)
  await fireEvent.change(sel(c, '#controlProgram'), { target: { value: program } })
}

async function fillConnect(c: HTMLElement) {
  await pickDeviceAndProgram(c)
  await fireEvent.input(inp(c, '#controlDuration'), { target: { value: '5' } })
}

async function sendAndConfirm(c: HTMLElement) {
  await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))
}

const result = (c: HTMLElement) => c.querySelector('#controlResult') as HTMLElement

describe('DerControl: Send/Confirm posts exactly once (criterion 2)', () => {
  it('Send posts nothing and names device and program; Confirm posts one request, and a rapid second click adds none', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created())
    const { container } = render(DerControl, { devices })
    await fillConnect(container)

    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    const bar = await screen.findByTestId('der-control-confirm')
    expect(bar).toHaveTextContent(
      'Connect on device 167261211635, program fixture program, starting now, for 5 min.',
    )
    expect(post).not.toHaveBeenCalled()

    const confirm = screen.getByRole('button', { name: 'Confirm' })
    confirm.click()
    confirm.click() // same tick: the first click has not re-rendered yet
    await waitFor(() => expect(result(container)).toHaveTextContent('Stored'))
    expect(post).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull()
  })
})

describe('DerControl: a pending confirmation follows the selection (fix round item 1)', () => {
  it('changing the device after Send drops the confirmation; nothing posts', async () => {
    const { post } = mockApi()
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await screen.findByRole('button', { name: 'Confirm' })

    await pickDevice(container, '1', 1)

    await waitFor(() => expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull())
    expect(screen.getByRole('button', { name: 'Send' })).toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  })

  it('changing the program after Send drops the confirmation; Send again names the new program', async () => {
    const { post } = mockApi()
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await screen.findByRole('button', { name: 'Confirm' })

    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })

    await waitFor(() => expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull())
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByTestId('der-control-confirm')).toHaveTextContent('program second program')
    expect(post).not.toHaveBeenCalled()
  })

  it('a result shown for one selection is cleared when the selection changes', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created())
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)
    await waitFor(() => expect(result(container)).toHaveTextContent('Stored'))

    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })

    await waitFor(() => expect(result(container)).toHaveTextContent(''))
    expect(result(container).textContent).toBe('')
  })
})

describe('DerControl: posted bodies wired from the form (criterion 1)', () => {
  it('maxLimW posts the percent in hundredths', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created())
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await fireEvent.change(sel(container, '#controlType'), { target: { value: 'maxLimW' } })
    await fireEvent.input(inp(container, '#controlValue'), { target: { value: '45.5' } })
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '5' } })
    await sendAndConfirm(container)

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/der/controls', {
      derProgramHref: PROG_A,
      type: 'maxLimW',
      durationSeconds: 300,
      maxLimW: 4550,
    })
  })

  it('fixedPFInjectW posts thousandths with excitation false by default', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created())
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await fireEvent.change(sel(container, '#controlType'), { target: { value: 'fixedPFInjectW' } })
    await fireEvent.input(inp(container, '#controlValue'), { target: { value: '0.95' } })
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '5' } })
    await sendAndConfirm(container)

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/der/controls', {
      derProgramHref: PROG_A,
      type: 'fixedPFInjectW',
      durationSeconds: 300,
      powerFactor: { displacement: 950, excitation: false },
    })
  })

  it('choosing Under-excited posts excitation true, and the summary says under-excited', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created())
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await fireEvent.change(sel(container, '#controlType'), { target: { value: 'fixedPFInjectW' } })
    await fireEvent.input(inp(container, '#controlValue'), { target: { value: '0.9' } })
    await fireEvent.change(sel(container, '#controlExcitation'), { target: { value: 'true' } })
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '5' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    expect(await screen.findByTestId('der-control-confirm')).toHaveTextContent('power factor to 0.900 (under-excited)')
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0]?.[1]).toMatchObject({ powerFactor: { displacement: 900, excitation: true } })
  })

  it('words the two excitation choices as the criterion specifies, false first', async () => {
    mockApi()
    const { container } = render(DerControl, { devices })
    await fireEvent.change(sel(container, '#controlType'), { target: { value: 'fixedPFInjectW' } })
    const options = Array.from(sel(container, '#controlExcitation').options).map((o) => [o.value, o.textContent])
    expect(options).toEqual([
      ['false', 'Over-excited: DER injects reactive power'],
      ['true', 'Under-excited: DER absorbs reactive power'],
    ])
  })

  it('a limit with a third decimal is refused on Send, before any confirmation', async () => {
    const { post } = mockApi()
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await fireEvent.change(sel(container, '#controlType'), { target: { value: 'maxLimW' } })
    await fireEvent.input(inp(container, '#controlValue'), { target: { value: '45.555' } })
    await fireEvent.input(inp(container, '#controlDuration'), { target: { value: '5' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(result(container)).toHaveTextContent('at most 2 decimals'))
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull()
    expect(post).not.toHaveBeenCalled()
  })
})

describe('DerControl: result area (criterion 3)', () => {
  it('on success shows stored time, mRID, status, status time, start and a nominal read estimate, never "sent"', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created())
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)

    await waitFor(() => expect(result(container)).toHaveTextContent('AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'))
    const text = result(container).textContent ?? ''
    expect(text).toContain(`Stored AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA at ${fmtTime(1000)}`) // creationTime
    expect(text).toContain(`starts ${fmtTime(5000)}`) // interval.start
    expect(text).toContain(`scheduled (as of ${fmtTime(3000)})`) // eventStatus.dateTime
    expect(text).toContain(fmtTime(1900)) // creationTime + 900
    expect(text).toMatch(/nominal estimate/i)
    expect(text).toContain('not a delivery')
    expect(text).not.toMatch(/\bsent\b/i)
    expect(text).not.toMatch(/restart/i) // persisted: true
  })

  it('names how many earlier controls the new one superseded', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created({ supersedes: ['X1', 'X2'] }))
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)
    await waitFor(() => expect(result(container)).toHaveTextContent('Superseded 2 earlier controls'))
  })

  it('on refusal shows the server error text and reloads the table', async () => {
    const { post, controlGets } = mockApi()
    post.mockResolvedValue({ ok: false, error: 'maxLimW: must be 0 to 10000', status: 400 })
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await waitFor(() => expect(controlGets()).toBeGreaterThan(0))
    const before = controlGets()
    await sendAndConfirm(container)

    await waitFor(() => expect(result(container)).toHaveTextContent('maxLimW: must be 0 to 10000'))
    expect(result(container).textContent).not.toMatch(/\bsent\b/i)
    await waitFor(() => expect(controlGets()).toBeGreaterThan(before))
  })

  it('on network failure shows an error, not a stored claim', async () => {
    const { post } = mockApi()
    post.mockResolvedValue({ ok: false, error: 'network error', status: 0 })
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)

    await waitFor(() => expect(result(container)).toHaveTextContent('network error'))
    expect(result(container).textContent).not.toMatch(/\bsent\b/i)
    expect(result(container).textContent).not.toContain('Stored')
  })

  it('a controlKept 500 says the control was kept and is live, names its mRID and href, and reloads the table', async () => {
    const { post, controlGets } = mockApi()
    post.mockResolvedValue({
      ok: false,
      status: 500,
      error: 'control may be live: its write could not be undone',
      body: {
        error: 'control may be live: its write could not be undone',
        mRID: 'KEPTKEPTKEPTKEPTKEPTKEPTKEPTKEPT',
        href: '/edev/0/fsa/0/derp/0/derc/kept1',
        controlKept: true,
      },
    })
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await waitFor(() => expect(controlGets()).toBeGreaterThan(0))
    const before = controlGets()
    await sendAndConfirm(container)

    await waitFor(() => expect(result(container)).toHaveTextContent('KEPTKEPTKEPTKEPTKEPTKEPTKEPTKEPT'))
    const text = result(container).textContent ?? ''
    expect(text).toContain('control may be live: its write could not be undone. ')
    expect(text).toContain('/edev/0/fsa/0/derp/0/derc/kept1')
    expect(text).toMatch(/kept/i)
    expect(text).toMatch(/live/i)
    await waitFor(() => expect(controlGets()).toBeGreaterThan(before))
  })
})

describe('DerControl: not persisted (criterion 4)', () => {
  it('says a restart forgets the control when persisted is false', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created({ persisted: false }))
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)
    await waitFor(() => expect(result(container)).toHaveTextContent('A server restart forgets this control.'))
  })
})

describe('DerControl: controls table (criterion 5)', () => {
  const now = Math.floor(Date.now() / 1000)
  const rows = {
    device: '0',
    controls: [
      item('SCHED', 'scheduled', now + 3600, 600),
      item('ACTIVE', 'active', now - 10, 3600),
      item('ENDED', 'active', now - 7200, 60),
      item('CANCELLED', 'cancelled', now - 10, 3600),
      item('SUPERSEDED', 'superseded', now - 10, 3600),
    ],
  } satisfies DERControlListResponse

  it('lists type, value, start, end, status and response counts labelled as device-reported', async () => {
    mockApi({ controlsFor: () => ({ ok: true, data: rows, serverTime: now * 1000 }) })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))

    const row = container.querySelector('[data-mrid="SCHED"]') as HTMLElement
    const cells = Array.from(row.querySelectorAll('td')).map((td) => td.textContent?.trim())
    expect(cells[0]).toBe('maxLimW')
    expect(cells[1]).toBe('45.50%')
    expect(cells[2]).toBe(fmtTime(now + 3600))
    expect(cells[3]).toBe(fmtTime(now + 3600 + 600)) // End = start + duration
    expect(cells[4]).toBe('scheduled')
    expect(cells[5]).toContain('2 reported')
    expect(container.querySelector('table')).toHaveTextContent('reported by device, not verified by server')
  })

  it('offers Cancel on Scheduled and on Active-not-ended rows only', async () => {
    mockApi({ controlsFor: () => ({ ok: true, data: rows, serverTime: now * 1000 }) })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))

    for (const present of ['SCHED', 'ACTIVE']) {
      expect(screen.queryByTestId(`der-control-cancel-${present}`)).not.toBeNull()
    }
    for (const absent of ['ENDED', 'CANCELLED', 'SUPERSEDED']) {
      expect(screen.queryByTestId(`der-control-cancel-${absent}`)).toBeNull()
    }
  })

  it('cancels only after a second confirmation, posts the typed reason, then reloads the table', async () => {
    const { post, controlGets } = mockApi({ controlsFor: () => ({ ok: true, data: rows, serverTime: now * 1000 }) })
    post.mockResolvedValue({ ok: true, data: {} })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
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
    const { post } = mockApi({ controlsFor: () => ({ ok: true, data: rows, serverTime: now * 1000 }) })
    post.mockResolvedValue({ ok: true, data: {} })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))

    await fireEvent.click(screen.getByTestId('der-control-cancel-ACTIVE'))
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(post).not.toHaveBeenCalled()
    expect(screen.getByTestId('der-control-cancel-ACTIVE')).toBeInTheDocument()

    await fireEvent.click(screen.getByTestId('der-control-cancel-ACTIVE'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-ACTIVE'))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/der/controls/ACTIVE/cancel', {}))
  })

  it('shows a cancel refusal and reloads the table', async () => {
    const { post, controlGets } = mockApi({ controlsFor: () => ({ ok: true, data: rows, serverTime: now * 1000 }) })
    post.mockResolvedValue({ ok: false, error: 'control already ended', status: 409 })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    const before = controlGets()

    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-SCHED'))

    await waitFor(() => expect(screen.getByTestId('der-control-cancel-result')).toHaveTextContent('control already ended'))
    await waitFor(() => expect(controlGets()).toBeGreaterThan(before))
  })

  it('a cancel 500 that names a kept control says so instead of "Cancel failed"', async () => {
    const { post } = mockApi({ controlsFor: () => ({ ok: true, data: rows, serverTime: now * 1000 }) })
    post.mockResolvedValue({
      ok: false,
      status: 500,
      error: 'cancellation may be recorded: its write could not be undone',
      body: { error: 'x', mRID: 'SCHED', href: '/edev/0/fsa/0/derp/0/derc/SCHED', controlKept: true },
    })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(5))
    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-SCHED'))

    const box = await screen.findByTestId('der-control-cancel-result')
    // The server's own words, a separator, then what the page knows: the
    // control still exists. It does not claim the control is live.
    expect(box.textContent).toContain('cancellation may be recorded: its write could not be undone. ')
    expect(box.textContent).toContain('still exists')
    expect(box.textContent).not.toMatch(/\blive\b/i)
    expect(box.textContent).not.toContain('Cancel failed')
  })
})

describe('DerControl: failed reads are errors, not empty results (fix round item 2)', () => {
  it('a failed programs GET shows the server error, not "no DER programs"', async () => {
    mockApi({ programsFor: () => ({ ok: false, error: 'internal error', status: 500 }) })
    const { container } = render(DerControl, { devices })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })

    expect(await screen.findByTestId('der-control-programs-error')).toHaveTextContent('internal error')
    expect(screen.queryByTestId('der-control-no-programs')).toBeNull()
  })

  it('a network failure on the programs GET is an error too', async () => {
    mockApi({ programsFor: () => ({ ok: false, error: 'network error', status: 0 }) })
    const { container } = render(DerControl, { devices })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    expect(await screen.findByTestId('der-control-programs-error')).toHaveTextContent('network error')
    expect(screen.queryByTestId('der-control-no-programs')).toBeNull()
  })

  it('a failed controls GET shows the server error, not an empty table', async () => {
    mockApi({ controlsFor: () => ({ ok: false, error: 'internal error', status: 500 }) })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)

    expect(await screen.findByTestId('der-control-controls-error')).toHaveTextContent('internal error')
    expect(container.textContent).not.toContain('No admin-issued controls')
    expect(screen.queryAllByTestId('der-control-row')).toHaveLength(0)
  })

  it('a successful empty controls list still says there are none, with no error', async () => {
    mockApi()
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(container.textContent).toContain('No admin-issued controls'))
    expect(screen.queryByTestId('der-control-controls-error')).toBeNull()
  })

  it('an error clears when the next read succeeds', async () => {
    let fail = true
    mockApi({
      programsFor: (id) => (fail ? { ok: false, error: 'internal error', status: 500 } : { ok: true, data: id === '0' ? progs0 : progs1 }),
    })
    const { container } = render(DerControl, { devices })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await screen.findByTestId('der-control-programs-error')
    fail = false
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(screen.queryByTestId('der-control-programs-error')).toBeNull())
    expect(sel(container, '#controlProgram').options).toHaveLength(2)
  })
})

describe('DerControl: stale-response guards (fix round item 2)', () => {
  it('a slow programs reply for the old device cannot overwrite the new device', async () => {
    const slow = deferred<unknown>()
    mockApi({ programsFor: (id) => (id === '0' ? slow.promise : { ok: true, data: progs1 }) })
    const { container } = render(DerControl, { devices })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(sel(container, '#controlProgram').options).toHaveLength(2))

    slow.resolve({ ok: true, data: progs0 })
    await new Promise((r) => setTimeout(r, 20))

    const labels = Array.from(sel(container, '#controlProgram').options).map((o) => o.textContent)
    expect(labels).toEqual(['(pick DER program)', 'other device program'])
  })

  it('a slow failing programs reply for the old device raises no error for the new one', async () => {
    const slow = deferred<unknown>()
    mockApi({ programsFor: (id) => (id === '0' ? slow.promise : { ok: true, data: progs1 }) })
    const { container } = render(DerControl, { devices })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '1' } })
    await waitFor(() => expect(sel(container, '#controlProgram').options).toHaveLength(2))

    slow.resolve({ ok: false, error: 'late failure', status: 500 })
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByTestId('der-control-programs-error')).toBeNull()
  })

  it('a slow controls reply for the old program cannot overwrite the new program\'s table', async () => {
    const slow = deferred<unknown>()
    const fast: DERControlListResponse = { device: '0', controls: [item('FASTROW', 'scheduled', Math.floor(Date.now() / 1000) + 60, 60)] }
    const stale: DERControlListResponse = { device: '0', controls: [item('STALEROW', 'scheduled', Math.floor(Date.now() / 1000) + 60, 60)] }
    mockApi({ controlsFor: (_d, p) => (p === PROG_A ? slow.promise : { ok: true, data: fast }) })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container, PROG_A)
    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })
    await waitFor(() => expect(container.querySelector('[data-mrid="FASTROW"]')).toBeTruthy())

    slow.resolve({ ok: true, data: stale })
    await new Promise((r) => setTimeout(r, 20))
    expect(container.querySelector('[data-mrid="STALEROW"]')).toBeNull()
    expect(container.querySelector('[data-mrid="FASTROW"]')).toBeTruthy()
  })
})

describe('DerControl: no DER programs (criterion 6)', () => {
  it('tells the operator programs come from the boot fixture', async () => {
    mockApi({ programsFor: () => ({ ok: true, data: { device: '0', programs: [] } }) })
    const { container } = render(DerControl, { devices })
    await fireEvent.change(sel(container, '#controlDevice'), { target: { value: '0' } })

    const note = await screen.findByTestId('der-control-no-programs')
    expect(note).toHaveTextContent('boot fixture')
    expect(note).toHaveTextContent('cannot be created in the admin UI')
    expect(screen.queryByTestId('der-control-programs-error')).toBeNull()
  })
})

describe('DerControl: ids and the removed stub (criterion 7)', () => {
  it('keeps controlType, controlValue and controlResult; drops the stub note', () => {
    mockApi()
    const { container } = render(DerControl, { devices })
    expect(container.querySelector('#controlType')).toBeTruthy()
    expect(container.querySelector('#controlValue')).toBeTruthy()
    expect(container.querySelector('#controlResult')).toBeTruthy()
    expect(container.querySelector('[data-testid="der-control-stub-note"]')).toBeNull()
    expect(container.textContent).not.toContain('admin DER control route')
  })
})

describe('DerControl: a reply after a selection change names its own target (fix round 2 item 1)', () => {
  it('disables both selects while a create is in flight', async () => {
    const held = deferred<unknown>()
    const { post } = mockApi()
    post.mockReturnValue(held.promise as never)
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)

    await waitFor(() => expect(sel(container, '#controlDevice')).toBeDisabled())
    expect(sel(container, '#controlProgram')).toBeDisabled()
    held.resolve(created())
    await waitFor(() => expect(sel(container, '#controlDevice')).not.toBeDisabled())
  })

  it('a create reply held across a device change is named with the device and program it was for', async () => {
    const held = deferred<unknown>()
    const { post } = mockApi()
    post.mockReturnValue(held.promise as never)
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)
    await pickDevice(container, '1', 1)

    held.resolve(created())
    await waitFor(() => expect(result(container)).toHaveTextContent('Stored'))
    expect(result(container).textContent).toContain('For device 167261211635, program fixture program: ')
  })

  it('a reply for the current selection carries no such prefix', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created())
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)
    await waitFor(() => expect(result(container)).toHaveTextContent('Stored'))
    expect(result(container).textContent).not.toContain('For device')
  })

  it('a kept-control 500 held across a program change does not send the operator to the table', async () => {
    const held = deferred<unknown>()
    const { post } = mockApi()
    post.mockReturnValue(held.promise as never)
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)
    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })

    held.resolve({
      ok: false,
      status: 500,
      error: 'control may be live: its write could not be undone',
      body: { error: 'x', mRID: 'KEPT', href: '/edev/0/fsa/0/derp/0/derc/kept1', controlKept: true },
    })
    await waitFor(() => expect(result(container)).toHaveTextContent('KEPT'))
    const text = result(container).textContent ?? ''
    expect(text).toContain('For device 167261211635, program fixture program: ')
    expect(text).toContain('/edev/0/fsa/0/derp/0/derc/kept1')
    expect(text).not.toMatch(/find it in the table/i)
    expect(text).toContain('Select that device and program')
  })

  it('a cancel refusal held across a program change is shown in the result area, named with its target', async () => {
    const held = deferred<unknown>()
    const row = item('SCHED', 'scheduled', Math.floor(Date.now() / 1000) + 3600, 600)
    const { post } = mockApi({ controlsFor: () => ({ ok: true, data: { device: '0', controls: [row] } }) })
    post.mockReturnValue(held.promise as never)
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-SCHED'))
    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })

    held.resolve({ ok: false, error: 'control already ended', status: 409 })
    await waitFor(() => expect(result(container)).toHaveTextContent('control already ended'))
    expect(result(container).textContent).toContain(
      'For device 167261211635, program fixture program, control SCHED: Cancel failed: control already ended',
    )
  })
})

describe('DerControl: selection reset and reload details (fix round 2 item 2)', () => {
  const soon = Math.floor(Date.now() / 1000) + 3600
  const rowsOnce = { device: '0', controls: [item('SCHED', 'scheduled', soon, 600)] }

  it('changing the program closes an open cancel confirmation', async () => {
    mockApi({ controlsFor: () => ({ ok: true, data: rowsOnce }) })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    expect(screen.getByTestId('der-control-confirm-cancel-SCHED')).toBeInTheDocument()

    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })

    await waitFor(() => expect(screen.getByTestId('der-control-cancel-SCHED')).toBeInTheDocument())
    expect(screen.queryByTestId('der-control-confirm-cancel-SCHED')).toBeNull()
  })

  it('changing the program clears a shown cancel failure', async () => {
    const { post } = mockApi({ controlsFor: () => ({ ok: true, data: rowsOnce }) })
    post.mockResolvedValue({ ok: false, error: 'control already ended', status: 409 })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    await fireEvent.click(screen.getByTestId('der-control-cancel-SCHED'))
    await fireEvent.click(screen.getByTestId('der-control-confirm-cancel-SCHED'))
    await screen.findByTestId('der-control-cancel-result')

    await fireEvent.change(sel(container, '#controlProgram'), { target: { value: PROG_B } })

    await waitFor(() => expect(screen.queryByTestId('der-control-cancel-result')).toBeNull())
  })

  it('a controls error clears when the reload after a create error succeeds', async () => {
    let n = 0
    const { post } = mockApi({
      controlsFor: () => (++n === 1 ? { ok: false, error: 'internal error', status: 500 } : { ok: true, data: noControls }),
    })
    post.mockResolvedValue({ ok: false, error: 'maxLimW: must be 0 to 10000', status: 400 })
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await screen.findByTestId('der-control-controls-error')

    await sendAndConfirm(container)

    await waitFor(() => expect(screen.queryByTestId('der-control-controls-error')).toBeNull())
    expect(container.textContent).toContain('No admin-issued controls')
  })

  it('says "Superseded 1 earlier control" in the singular', async () => {
    const { post } = mockApi()
    post.mockResolvedValue(created({ supersedes: ['X1'] }))
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await sendAndConfirm(container)
    await waitFor(() => expect(result(container)).toHaveTextContent('Superseded 1 earlier control.'))
  })
})

describe('DerControl: "now" for Cancel comes from the server clock (fix round 2 item 4)', () => {
  const browserNow = Math.floor(Date.now() / 1000)
  // Ends 50 s after the server's clock but 100 s before the browser's.
  const serverNow = browserNow - 150
  const live = { device: '0', controls: [item('LIVE', 'active', serverNow - 10, 60)] }

  it('keeps Cancel on an Active row the browser clock alone would call ended', async () => {
    mockApi({ controlsFor: () => ({ ok: true, data: live, serverTime: serverNow * 1000 }) })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    expect(screen.getByTestId('der-control-cancel-LIVE')).toBeInTheDocument()
  })

  it('hides Cancel once the server clock is past the end, and refreshes that clock on each reload', async () => {
    let serverMs = (serverNow - 10) * 1000 // before the end: Cancel shown
    const { post } = mockApi({ controlsFor: () => ({ ok: true, data: live, serverTime: serverMs }) })
    post.mockResolvedValue({ ok: false, error: 'maxLimW: must be 0 to 10000', status: 400 })
    const { container } = render(DerControl, { devices })
    await fillConnect(container)
    await waitFor(() => expect(screen.getByTestId('der-control-cancel-LIVE')).toBeInTheDocument())

    serverMs = (serverNow + 500) * 1000 // the next read says the interval has ended
    await sendAndConfirm(container) // refusal reloads the table

    await waitFor(() => expect(screen.queryByTestId('der-control-cancel-LIVE')).toBeNull())
  })

  it('with no server time the Active row keeps Cancel and the server decides', async () => {
    const ended = { device: '0', controls: [item('OLD', 'active', browserNow - 7200, 60)] }
    mockApi({ controlsFor: () => ({ ok: true, data: ended }) })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getAllByTestId('der-control-row')).toHaveLength(1))
    expect(screen.getByTestId('der-control-cancel-OLD')).toBeInTheDocument()
  })
})
