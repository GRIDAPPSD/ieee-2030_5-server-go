// The dispatch pane against mocked reads and a mocked create. What it must
// get right: nothing is sent before Confirm; the body holds the operator's
// target with its sign as typed or as the server suggested it, and the grant
// being executed; a 409 is shown with its code and the mRID it names; an
// unknown outcome is never re-sent and locks sending until the controls list
// is read again; a missing value shows as missing.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/svelte'
import DispatchPane from './DispatchPane.svelte'
import * as api from '../lib/api'

type Res =
  | { ok: true; data: unknown; serverTime?: number }
  | { ok: false; error: string; status: number; body?: unknown }

const LFDI = 'AGG00000000000000000000000000000000001'
const GRANT_MRID = '9D1E3B5C00000000000000000000A1B2'
const PROGRAM = '/edev/4/derp/1'

const zero = { sum: 0, unreported: 1, stale: 0 }
const FLEET = {
  aggregatorLFDI: LFDI,
  devices: [{ lfdi: 'DEV-LFDI-4', edevId: '4', href: '/edev/4', measurements: {} }],
  rollup: { deviceCount: 1, connected: 0, alarmed: 0, stale: 0, p: zero, q: zero, statWAvail: zero, statVarAvail: zero },
}

function grantWire(opts: { direction?: string; target?: number; remaining?: number | null } = {}) {
  const { direction = 'discharge', target = 8000, remaining = 3000 } = opts
  return {
    edevId: '4',
    frqId: 'frq-1',
    response: {
      id: 'frq-1-r1',
      href: '/edev/4/frp/frq-1-r1',
      mRID: GRANT_MRID,
      subject: 'S',
      creationTime: 1790000000,
      interval: { start: 1790000000, duration: 1800 },
      energyAvailable: { value: 4000, multiplier: 0 },
      powerAvailable: { value: 8000, multiplier: 0 },
      direction,
      eventStatus: { currentStatus: 1, status: 'active', dateTime: 1790000000 },
      cancelReason: null,
      answeredBy: { kind: 'operator', admission: null, principal: null, at: 1790000000 },
      cancelledBy: null,
      executions: [],
      energyCommittedWh: 1000,
      energyRemainingWh: remaining,
    },
    suggestedTargetW: { value: target, multiplier: 0 },
  }
}

function controlItem(over: Record<string, unknown> = {}) {
  return {
    mRID: 'NEWCTL01',
    href: '/edev/4/derp/1/derc/1',
    derProgramHref: PROGRAM,
    derControlListHref: '/edev/4/derp/1/derc',
    type: 'targetW',
    description: '',
    derControlBase: { opModTargetW: { value: 8000, multiplier: 0 } },
    creationTime: 1790000000,
    interval: { start: 1790000000, duration: 1800 },
    eventStatus: { currentStatus: 0, status: 'scheduled', dateTime: 1790000000 },
    executesGrant: GRANT_MRID,
    responses: { total: 2, byStatus: { Received: 1, Started: 1 } },
    ...over,
  }
}

interface World {
  grants: unknown[]
  controls: unknown[]
  controlReads: number
  failControls: boolean
  grantsReply: Res | null
  now: number
  fleet: unknown
  programs: unknown
  controlsReply: Res | null
  reads: string[]
}

function mockReads(world: Partial<World> = {}) {
  const w: World = { grants: [grantWire()], controls: [], controlReads: 0,
    failControls: false,
    grantsReply: null,
    // Before the grant's start, so a grant is not "already started" unless a test says so.
    now: 1789990000,
    fleet: FLEET,
    programs: [{ href: PROGRAM, mRID: 'P1', description: 'Dispatch program', primacy: 0, derControlListHref: PROGRAM + '/derc' }],
    controlsReply: null,
    reads: [],
    ...world,
  }
  const spy = vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    w.reads.push(path)
    if (path === '/api/derms/fleets') return { ok: true, data: [w.fleet] }
    if (path.startsWith('/api/derms/grants')) {
      return w.grantsReply ?? { ok: true, data: { aggregatorLFDI: LFDI, now: w.now, grants: w.grants } }
    }
    if (/^\/api\/devices\/[^/]+\/der-programs$/.test(path)) {
      return { ok: true, data: { device: path.split('/')[3], programs: w.programs } }
    }
    if (path.startsWith('/api/der/controls')) {
      w.controlReads++
      if (w.failControls) return { ok: false, error: 'boom', status: 500 }
      if (w.controlsReply !== null) return w.controlsReply
      return { ok: true, data: { device: '4', controls: w.controls } }
    }
    throw new Error('unexpected read ' + path)
  }) as never)
  return { w, spy }
}

const created = {
  ...controlItem(),
  supersedes: [],
  notificationAttempted: true,
  persisted: true,
}

function mockWrite(res: Res | Promise<Res>) {
  return vi.spyOn(api, 'postJSON').mockImplementation((async () => res) as never)
}

async function pickFleet() {
  const select = await screen.findByTestId('dispatch-fleet')
  await fireEvent.change(select, { target: { value: LFDI } })
}

async function pickGrant() {
  await pickFleet()
  const select = await screen.findByTestId('dispatch-grant')
  await fireEvent.change(select, { target: { value: 'frq-1-r1' } })
  await waitFor(() => expect((screen.getByTestId('dispatch-program') as HTMLSelectElement).value).toBe(PROGRAM))
}

async function pickGrantOnly() {
  await pickFleet()
  await fireEvent.change(await screen.findByTestId('dispatch-grant'), { target: { value: 'frq-1-r1' } })
}

async function review() {
  await fireEvent.click(screen.getByRole('button', { name: 'Review' }))
}

async function confirmAndWait() {
  await review()
  await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
}

function mount() {
  return render(DispatchPane)
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('DispatchPane, executing a grant', () => {
  it('prefills the interval and signed target from the grant and shows the energy it has left', async () => {
    mockReads()
    mount()
    await pickGrant()
    expect((screen.getByTestId('dispatch-start') as HTMLInputElement).value).toBe('2026-09-21T14:13:20Z')
    expect((screen.getByTestId('dispatch-duration') as HTMLInputElement).value).toBe('1800')
    expect((screen.getByTestId('dispatch-power') as HTMLInputElement).value).toBe('8000')
    expect(screen.getByTestId('dispatch-energy-left')).toHaveTextContent('Energy left in this grant: 3,000 Wh')
    expect(screen.getByTestId('dispatch-suggested')).toHaveTextContent('8,000 W (DER frame: positive discharges, negative charges)')
    expect(screen.getByTestId('dispatch-grants-age')).toHaveTextContent('fetched 0s ago')
  })

  it('sends nothing before Confirm, and the confirm step names fleet, grant, interval and target', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: created })
    mount()
    await pickGrant()
    await review()
    const text = (await screen.findByTestId('dispatch-confirm-text')).textContent ?? ''
    expect(text).toContain(LFDI.substring(0, 16))
    expect(text).toContain(GRANT_MRID)
    expect(text).toContain('request frq-1')
    expect(text).toContain('2026-09-21 14:13:20 UTC for 30 min')
    expect(text).toContain('target power 8,000 W (DER frame')
    expect(post).not.toHaveBeenCalled()
  })

  it('posts the grant mRID, interval and the target exactly as the server suggested', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: created })
    mount()
    await pickGrant()
    await confirmAndWait()
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toBe('/api/der/controls')
    expect(post.mock.calls[0][1]).toEqual({
      derProgramHref: PROGRAM,
      type: 'targetW',
      targetW: { value: 8000, multiplier: 0 },
      durationSeconds: 1800,
      startTime: 1790000000,
      executesGrant: GRANT_MRID,
    })
  })

  it('keeps the sign of a charge grant: shows and posts a negative target, never a flipped one', async () => {
    mockReads({ grants: [grantWire({ direction: 'charge', target: -5000 })] })
    const post = mockWrite({ ok: true, data: created })
    mount()
    await pickGrant()
    expect((screen.getByTestId('dispatch-power') as HTMLInputElement).value).toBe('-5000')
    expect(screen.getByTestId('dispatch-grant-detail')).toHaveTextContent('charge')
    await confirmAndWait()
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][1]).toMatchObject({ targetW: { value: -5000, multiplier: 0 } })
  })

  it('shows a missing energy figure as missing, not as zero', async () => {
    mockReads({ grants: [grantWire({ remaining: null })] })
    mount()
    await pickGrant()
    expect(screen.getByTestId('dispatch-energy-left')).toHaveTextContent('Energy left in this grant: missing')
  })

  it('says grants are not available when the route is not there', async () => {
    mockReads({ grantsReply: { ok: false, error: 'not found', status: 404 } })
    mount()
    await pickFleet()
    expect(await screen.findByTestId('dispatch-grants-error')).toHaveTextContent('not available on this server yet')
  })
})

describe('DispatchPane, plain dispatch', () => {
  it('posts the typed signed target with no grant and no start', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: { ...created, executesGrant: null } })
    mount()
    await pickFleet()
    await fireEvent.click(screen.getByRole('radio', { name: 'Plain dispatch' }))
    await fireEvent.change(await screen.findByTestId('dispatch-device'), { target: { value: '4' } })
    await waitFor(() => expect((screen.getByTestId('dispatch-program') as HTMLSelectElement).value).toBe(PROGRAM))
    await fireEvent.input(screen.getByTestId('dispatch-duration'), { target: { value: '600' } })
    await fireEvent.input(screen.getByTestId('dispatch-power'), { target: { value: '-2500' } })
    await review()
    expect(await screen.findByTestId('dispatch-confirm-text')).toHaveTextContent('A plain dispatch, carrying out no grant.')
    expect(post).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    const body = post.mock.calls[0][1] as Record<string, unknown>
    expect(body).toEqual({ derProgramHref: PROGRAM, type: 'targetW', targetW: { value: -2500, multiplier: 0 }, durationSeconds: 600 })
    expect('executesGrant' in body).toBe(false)
  })

  it('refuses an unreadable target before sending anything', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: created })
    mount()
    await pickFleet()
    await fireEvent.click(screen.getByRole('radio', { name: 'Plain dispatch' }))
    await fireEvent.change(await screen.findByTestId('dispatch-device'), { target: { value: '4' } })
    await waitFor(() => expect((screen.getByTestId('dispatch-program') as HTMLSelectElement).value).toBe(PROGRAM))
    await fireEvent.input(screen.getByTestId('dispatch-duration'), { target: { value: '600' } })
    await fireEvent.input(screen.getByTestId('dispatch-power'), { target: { value: '1.5' } })
    await review()
    expect(await screen.findByTestId('dispatch-form-error')).toHaveTextContent('whole number of watts')
    expect(screen.queryByTestId('dispatch-confirm')).toBeNull()
    expect(post).not.toHaveBeenCalled()
  })
})

describe('DispatchPane, after a create', () => {
  it('shows the stored control with its response counts, the grant it carries out, and no delivery claim', async () => {
    const { w } = mockReads()
    mockWrite({ ok: true, data: created })
    mount()
    await pickGrant()
    w.controls = [controlItem()]
    await confirmAndWait()
    const row = await screen.findByTestId('dispatch-control-row')
    expect(row).toHaveAttribute('data-created', 'true')
    expect(screen.getByTestId('dispatch-control-responses')).toHaveTextContent('2 reported (Received: 1, Started: 1)')
    expect(screen.getByTestId('dispatch-control-grant')).toHaveTextContent(GRANT_MRID)
    expect(row).toHaveTextContent('8,000 W (DER frame)')
    expect(screen.getByTestId('dispatch-result')).toHaveTextContent('Stored control NEWCTL01')
    expect(screen.getByTestId('dispatch-delivery-note')).toHaveTextContent('does not expose it yet')
  })

  it('reloads the grants so the energy left reflects the new control', async () => {
    const { w, spy } = mockReads()
    mockWrite({ ok: true, data: created })
    mount()
    await pickGrant()
    const before = spy.mock.calls.filter((c) => String(c[0]).startsWith('/api/derms/grants')).length
    w.grants = [grantWire({ remaining: 1200 })]
    await confirmAndWait()
    await waitFor(() => expect(screen.getByTestId('dispatch-energy-left')).toHaveTextContent('Energy left in this grant: 1,200 Wh'))
    expect(spy.mock.calls.filter((c) => String(c[0]).startsWith('/api/derms/grants')).length).toBe(before + 1)
  })
})

describe('DispatchPane, refusals and unknown outcomes', () => {
  it('shows a 409 with the server text, the code and the mRID it names, and offers Review again', async () => {
    mockReads()
    mockWrite({
      ok: false,
      status: 409,
      error: "targetW: exceeds the grant's powerAvailable",
      body: { error: "targetW: exceeds the grant's powerAvailable", code: 'power_exceeds', mRID: GRANT_MRID },
    })
    mount()
    await pickGrant()
    await confirmAndWait()
    const shown = await screen.findByTestId('dispatch-result')
    expect(shown).toHaveTextContent("targetW: exceeds the grant's powerAvailable")
    expect(shown).toHaveTextContent('(power_exceeds)')
    expect(shown).toHaveTextContent('Names ' + GRANT_MRID)
    expect(screen.getByRole('button', { name: 'Review' })).toBeEnabled()
  })

  it('never re-sends after an unknown outcome; only a read that started a write-timeout after the send lifts the lock', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { w } = mockReads()
    const post = mockWrite({ ok: false, status: 0, error: 'network error' })
    mount()
    await pickGrant()
    await confirmAndWait()
    expect(await screen.findByTestId('dispatch-result')).toHaveTextContent('Outcome unknown')
    // The read made at once succeeds, but it started before the write could have landed.
    await waitFor(() => expect(w.controlReads).toBeGreaterThanOrEqual(1))
    await waitFor(() => expect(screen.getByTestId('dispatch-controls-age')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    expect(screen.getByTestId('dispatch-lock')).toHaveTextContent(/Next read in \d+ s/)
    const readsBefore = w.controlReads
    await vi.advanceTimersByTimeAsync(15_000)
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    expect(w.controlReads).toBe(readsBefore)
    await vi.advanceTimersByTimeAsync(6_000)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Review' })).toBeEnabled())
    expect(w.controlReads).toBe(readsBefore + 1)
    expect(post).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  // The lock margin runs from the moment the outcome became unknown, not from
  // the send: after a 20 s timeout the server may still be writing.
  it('holds the lock for a full margin after the write timeout, and the read made at once does not lift it', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { w } = mockReads()
    const post = vi.spyOn(api, 'postJSON').mockImplementation((() =>
      new Promise<Res>((r) => setTimeout(() => r({ ok: false, status: 0, error: 'request timed out' }), 20_000))) as never)
    mount()
    await pickGrant()
    await confirmAndWait()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(await screen.findByTestId('dispatch-result')).toHaveTextContent('Outcome unknown')
    await waitFor(() => expect(w.controlReads).toBeGreaterThanOrEqual(1))
    await waitFor(() => expect(screen.getByTestId('dispatch-controls-age')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    await vi.advanceTimersByTimeAsync(19_000)
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    await vi.advanceTimersByTimeAsync(2_000)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Review' })).toBeEnabled())
    expect(post).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it.each([
    ['a network error', { ok: false, status: 0, error: 'network error' } as Res],
    ['a server error', { ok: false, status: 503, error: 'unavailable' } as Res],
    ['Stop waiting', null],
  ])('holds the lock for a full margin after %s that comes 5 s after the send', async (_name, reply) => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mockReads()
    vi.spyOn(api, 'postJSON').mockImplementation((() =>
      new Promise<Res>((r) => {
        if (reply !== null) setTimeout(() => r(reply), 5_000)
      })) as never)
    mount()
    await pickGrant()
    await confirmAndWait()
    await vi.advanceTimersByTimeAsync(5_000)
    if (reply === null) await fireEvent.click(await screen.findByRole('button', { name: 'Stop waiting' }))
    expect(await screen.findByTestId('dispatch-lock')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('dispatch-controls-age')).toBeInTheDocument())
    // 19 s after the outcome became unknown, 24 s after the send.
    await vi.advanceTimersByTimeAsync(19_000)
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    await vi.advanceTimersByTimeAsync(2_000)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Review' })).toBeEnabled())
    vi.useRealTimers()
  })

  it('a manual Reload before the unlock time does not lift the lock; one after it does', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { w } = mockReads()
    const post = mockWrite({ ok: false, status: 0, error: 'network error' })
    mount()
    await pickGrant()
    w.failControls = true
    await confirmAndWait()
    await waitFor(() => expect(screen.getByTestId('dispatch-controls-error')).toBeInTheDocument())
    w.failControls = false
    w.controls = [controlItem()]
    await fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(screen.getByTestId('dispatch-controls-age')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    await vi.advanceTimersByTimeAsync(21_000)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Review' })).toBeEnabled())
    expect(post).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it('does not lift the lock on a controls reply that has no list, and shows an error', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { w } = mockReads({ controlsReply: { ok: true, data: { device: '4' } } })
    mockWrite({ ok: false, status: 0, error: 'network error' })
    mount()
    await pickGrant()
    await confirmAndWait()
    await vi.advanceTimersByTimeAsync(21_000)
    expect(await screen.findByTestId('dispatch-controls-error')).toHaveTextContent('no controls list')
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    expect(screen.getByTestId('dispatch-lock')).toHaveTextContent('Waiting for a successful read')
    expect(screen.queryByText('No admin-issued controls for this program')).toBeNull()
    expect(w.controlReads).toBeGreaterThan(1)
    vi.useRealTimers()
  })

  it('names a kept control from a 500 and does not re-send', async () => {
    mockReads()
    const post = mockWrite({ ok: false, status: 500, error: 'x', body: { controlKept: true, mRID: 'KEPT1', href: '/edev/4/derp/1/derc/9' } })
    mount()
    await pickGrant()
    await confirmAndWait()
    expect(await screen.findByTestId('dispatch-result')).toHaveTextContent('KEPT1 (/edev/4/derp/1/derc/9)')
    expect(post).toHaveBeenCalledTimes(1)
  })
})

describe('DispatchPane, while a write is in flight', () => {
  it('locks Confirm and the selectors, and sends once however often Confirm is pressed', async () => {
    mockReads()
    let finish!: (r: Res) => void
    const post = mockWrite(new Promise<Res>((r) => (finish = r)))
    mount()
    await pickGrant()
    await confirmAndWait()
    await screen.findByTestId('dispatch-sending')
    const confirm = screen.getByRole('button', { name: 'Confirm' })
    expect(confirm).toBeDisabled()
    expect(screen.getByTestId('dispatch-fleet')).toBeDisabled()
    await fireEvent.click(confirm)
    await fireEvent.click(confirm)
    expect(post).toHaveBeenCalledTimes(1)
    finish({ ok: true, data: created })
    await waitFor(() => expect(screen.getByTestId('dispatch-result')).toHaveTextContent('Stored control NEWCTL01'))
  })

  it('aborts the write on Stop waiting and ignores the answer that lands later', async () => {
    mockReads()
    let finish!: (r: Res) => void
    let signal: AbortSignal | undefined
    vi.spyOn(api, 'postJSON').mockImplementation(((_p: string, _b: unknown, opts?: api.RequestOptions) => {
      signal = opts?.signal
      return new Promise<Res>((r) => (finish = r))
    }) as never)
    mount()
    await pickGrant()
    await confirmAndWait()
    await fireEvent.click(await screen.findByRole('button', { name: 'Stop waiting' }))
    expect(signal?.aborted).toBe(true)
    expect(await screen.findByTestId('dispatch-result')).toHaveTextContent('Stopped waiting')
    await waitFor(() => expect(screen.getByTestId('dispatch-controls-age')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    finish({ ok: true, data: created })
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.getByTestId('dispatch-result')).not.toHaveTextContent('Stored control')
    expect(screen.queryByTestId('dispatch-sending')).toBeNull()
  })

  it('drops an answer that lands after unmount without touching the page', async () => {
    mockReads()
    let finish!: (r: Res) => void
    let signal: AbortSignal | undefined
    vi.spyOn(api, 'postJSON').mockImplementation(((_p: string, _b: unknown, opts?: api.RequestOptions) => {
      signal = opts?.signal
      return new Promise<Res>((r) => (finish = r))
    }) as never)
    const view = mount()
    await pickGrant()
    await confirmAndWait()
    await screen.findByTestId('dispatch-sending')
    view.unmount()
    expect(signal?.aborted).toBe(true)
    finish({ ok: true, data: created })
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByTestId('dispatch-pane')).toBeNull()
  })
})

describe('DispatchPane, what the operator sees', () => {
  it('names the device with its LFDI, the program, the fleet and the grant in the confirm text', async () => {
    mockReads()
    mockWrite({ ok: true, data: created })
    mount()
    await pickGrant()
    await review()
    const text = (await screen.findByTestId('dispatch-confirm-text')).textContent ?? ''
    expect(text).toContain('device EndDevice 4 (LFDI DEV-LFDI-4)')
    expect(text).toContain('program Dispatch program')
    expect(text).toContain('It reaches that device only, not the whole fleet.')
    expect(text).toContain(GRANT_MRID)
  })

  it('says how many fleet devices cannot be offered', async () => {
    const fleet = { ...FLEET, devices: [...FLEET.devices, { lfdi: 'UNREGISTERED-1', measurements: {} }, { lfdi: 'UNREGISTERED-2', measurements: {} }] }
    mockReads({ fleet })
    mount()
    await pickFleet()
    expect(await screen.findByTestId('dispatch-unaddressable')).toHaveTextContent('2 of 3 fleet devices cannot be offered here: they were never registered as an EndDevice')
  })

  it('offers every device of a fleet larger than 50, not a first page', async () => {
    const many = Array.from({ length: 60 }, (_, i) => ({ lfdi: 'LFDI-' + i, edevId: String(i + 100), href: '/edev/' + (i + 100), measurements: {} }))
    mockReads({ fleet: { ...FLEET, devices: many, rollup: { ...FLEET.rollup, deviceCount: 60 } } })
    mount()
    await pickFleet()
    await fireEvent.click(screen.getByRole('radio', { name: 'Plain dispatch' }))
    const select = (await screen.findByTestId('dispatch-device')) as HTMLSelectElement
    const ids = Array.from(select.options).map((o) => o.value).filter((v) => v !== '')
    expect(ids).toHaveLength(60)
    expect(ids[59]).toBe('159')
    expect(screen.queryByTestId('dispatch-unaddressable')).toBeNull()
  })

  it('takes the device id for the program and control routes from the fleet entry', async () => {
    const fleet = { ...FLEET, devices: [{ lfdi: 'SOME-LFDI', edevId: '77', href: '/edev/77', measurements: {} }] }
    const { w } = mockReads({ fleet })
    mount()
    await pickFleet()
    await fireEvent.click(screen.getByRole('radio', { name: 'Plain dispatch' }))
    await fireEvent.change(await screen.findByTestId('dispatch-device'), { target: { value: '77' } })
    await waitFor(() => expect(w.reads).toContain('/api/devices/77/der-programs'))
    await waitFor(() => expect(w.reads.some((r) => r.startsWith('/api/der/controls?device=77&'))).toBe(true))
  })

  it('does not warn when every fleet device can be offered', async () => {
    mockReads()
    mount()
    await pickFleet()
    await screen.findByTestId('dispatch-grant')
    expect(screen.queryByTestId('dispatch-unaddressable')).toBeNull()
  })

  it('says why the program select is empty for a device with no programs', async () => {
    mockReads({ programs: [] })
    mount()
    await pickGrantOnly()
    expect(await screen.findByTestId('dispatch-no-programs')).toHaveTextContent('no DER programs')
  })

  it('shows an error, not an empty list, for a programs reply with no list', async () => {
    mockReads({ programs: undefined })
    mount()
    await pickGrantOnly()
    expect(await screen.findByTestId('dispatch-programs-error')).toHaveTextContent('no programs list')
    expect(screen.queryByTestId('dispatch-no-programs')).toBeNull()
  })

  it('moves a grant that has already started to now plus a margin, and says so', async () => {
    mockReads({ now: 1790000100 })
    mount()
    await pickGrant()
    const startText = (screen.getByTestId('dispatch-start') as HTMLInputElement).value
    expect(Date.parse(startText) / 1000).toBeGreaterThanOrEqual(1790000130)
    expect(Date.parse(startText) / 1000).toBeLessThan(1790000140)
    expect(screen.getByTestId('dispatch-prefill-note')).toHaveTextContent('already started')
  })

  it('shows an error, not Loading, when the first controls read fails', async () => {
    mockReads({ failControls: true })
    mount()
    await pickGrant()
    expect(await screen.findByTestId('dispatch-controls-error')).toBeInTheDocument()
    expect(screen.queryByText('Loading controls...')).toBeNull()
    expect(screen.getByText('Controls could not be read.')).toBeInTheDocument()
  })

  it('drops the picked grant, its form and its note when the refresh after a create no longer lists it', async () => {
    const { w } = mockReads({ now: 1790000100 })
    mockWrite({ ok: true, data: created })
    mount()
    await pickGrant()
    expect(screen.getByTestId('dispatch-prefill-note')).toBeInTheDocument()
    w.grants = []
    await confirmAndWait()
    await waitFor(() => expect(screen.queryByTestId('dispatch-grant-detail')).toBeNull())
    expect(screen.queryByTestId('dispatch-prefill-note')).toBeNull()
    expect(screen.queryByTestId('dispatch-confirm')).toBeNull()
    expect(screen.queryByTestId('dispatch-power')).toBeNull()
  })

  it('ignores a controls read for the previous device that lands after a device change', async () => {
    let releaseControls!: () => void
    const gate = new Promise<void>((r) => (releaseControls = r))
    let releasePrograms5!: () => void
    const gate5 = new Promise<void>((r) => (releasePrograms5 = r))
    const prog = (id: string) => ({ href: `/edev/${id}/derp/1`, mRID: 'P' + id, description: 'Prog ' + id, primacy: 0, derControlListHref: '' })
    const fleet = {
      ...FLEET,
      devices: [
        { lfdi: 'DEV-LFDI-4', edevId: '4', href: '/edev/4', measurements: {} },
        { lfdi: 'DEV-LFDI-5', edevId: '5', href: '/edev/5', measurements: {} },
      ],
    }
    vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
      if (path === '/api/derms/fleets') return { ok: true, data: [fleet] }
      if (path.startsWith('/api/derms/grants')) return { ok: true, data: { aggregatorLFDI: LFDI, now: 1789990000, grants: [] } }
      if (path === '/api/devices/4/der-programs') return { ok: true, data: { device: '4', programs: [prog('4')] } }
      if (path === '/api/devices/5/der-programs') {
        await gate5
        return { ok: true, data: { device: '5', programs: [prog('5')] } }
      }
      if (path.startsWith('/api/der/controls?device=4')) {
        await gate
        return { ok: true, data: { device: '4', controls: [controlItem({ mRID: 'DEVICE4CTL' })] } }
      }
      if (path.startsWith('/api/der/controls?device=5')) return new Promise(() => {})
      throw new Error('unexpected read ' + path)
    }) as never)
    render(DispatchPane)
    await pickFleet()
    await fireEvent.click(screen.getByRole('radio', { name: 'Plain dispatch' }))
    await fireEvent.change(await screen.findByTestId('dispatch-device'), { target: { value: '4' } })
    await waitFor(() => expect((screen.getByTestId('dispatch-program') as HTMLSelectElement).value).toBe('/edev/4/derp/1'))
    await fireEvent.change(screen.getByTestId('dispatch-device'), { target: { value: '5' } })
    releaseControls()
    await new Promise((r) => setTimeout(r, 0))
    releasePrograms5()
    await waitFor(() => expect((screen.getByTestId('dispatch-program') as HTMLSelectElement).value).toBe('/edev/5/derp/1'))
    expect(screen.queryByText('DEVICE4CTL')).toBeNull()
    expect(screen.queryByTestId('dispatch-control-row')).toBeNull()
  })
})
