// The operator actions in the queue pane, against mocked write calls. What
// they must get right: nothing is sent before Confirm; the body holds the
// operator's choice and magnitudes only; the row is replaced from the view
// the server returns; every refusal shows its code in plain words; buttons
// lock while a write is in flight; and an answer that lands after the
// operator stopped waiting, after unmount, or under an older load never
// reaches the row.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, within, cleanup, fireEvent, waitFor } from '@testing-library/svelte'
import RequestQueuePane from './RequestQueuePane.svelte'
import * as api from '../lib/api'
import fixture from '../lib/flowreservation.fixture.json'

type Res =
  | { ok: true; data: unknown; serverTime?: number }
  | { ok: false; error: string; status: number; body?: unknown }

const LFDI = fixture.aggregatorLFDI
const PENDING_BASE = '/api/derms/flow-reservations/4/frq-1790000000000000001/'

function copy() {
  return JSON.parse(JSON.stringify(fixture))
}

function mockReads(queue: unknown = fixture) {
  return vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) =>
    path === '/api/derms/fleets'
      ? { ok: true, data: [{ aggregatorLFDI: LFDI }] }
      : { ok: true, data: queue }) as never)
}

function mockWrite(res: Res | Promise<Res>) {
  return vi.spyOn(api, 'postJSON').mockImplementation((async () => res) as never)
}

// grantedView is the server's view of the pending request after it was
// granted with values the pending row never showed.
function grantedView() {
  const v = copy().requests[1]
  const pending = fixture.requests[0]
  v.edevId = pending.edevId
  v.frqId = pending.frqId
  v.requestHref = pending.requestHref
  v.request = pending.request
  v.responses = [v.responses[1]]
  v.responses[0].energyAvailable = { value: 7777, multiplier: 0 }
  v.tip = v.responses[0]
  return v
}

function deniedView() {
  const v = grantedView()
  v.state = 'denied'
  v.responses[0].interval = { start: 1790003600, duration: 0 }
  v.tip = v.responses[0]
  return v
}

async function rows() {
  return screen.findAllByTestId('frq-row')
}

async function press(scope: HTMLElement | typeof document.body, name: string) {
  await fireEvent.click(within(scope as HTMLElement).getByRole('button', { name }))
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  vi.setSystemTime(new Date(1_700_000_000 * 1000))
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('which actions a row offers', () => {
  it('offers answer actions on pending and revise or cancel on granted, nothing else', async () => {
    mockReads()
    render(RequestQueuePane)
    const [pending, granted] = await rows()
    expect(within(pending).getAllByRole('button').map((b) => b.textContent)).toEqual([
      'Grant as asked',
      'Grant adjusted',
      'Deny',
    ])
    expect(within(granted).getAllByRole('button').map((b) => b.textContent)).toEqual(['Revise', 'Cancel grant'])
  })

  it.each(['denied', 'cancelled', 'withdrawn', 'ended', 'overdue'])('offers no action on a %s request', async (state) => {
    const q = copy()
    q.requests[1].state = state
    q.requests = [q.requests[1]]
    mockReads(q)
    render(RequestQueuePane)
    const [row] = await rows()
    expect(within(row).queryAllByRole('button')).toHaveLength(0)
  })
})

describe('confirm step', () => {
  it('sends nothing until Confirm, and names the request and the effect first', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: grantedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant as asked')
    expect(post).not.toHaveBeenCalled()
    const text = screen.getByTestId('frq-confirm-text').textContent ?? ''
    expect(text).toContain('Grant the request exactly as asked')
    expect(text).toContain(' UTC for 30 min')
    expect(screen.getByTestId('frq-action').textContent).toContain('frq-1790000000000000001')
    await press(pending, 'Do not send')
    expect(post).not.toHaveBeenCalled()
    expect(screen.queryByTestId('frq-action')).toBeNull()
  })

  it('reaches the confirm step from an adjusted grant only through Review', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: grantedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant adjusted')
    expect(screen.queryByTestId('frq-confirm-text')).toBeNull()
    await fireEvent.input(screen.getByTestId('frq-in-energy'), { target: { value: '5000' } })
    await press(pending, 'Review')
    expect(screen.getByTestId('frq-confirm-text').textContent).toContain('energy 5000 Wh')
    expect(post).not.toHaveBeenCalled()
  })

  it('shows an unsendable input as an error on the form and sends nothing', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: grantedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant adjusted')
    await fireEvent.input(screen.getByTestId('frq-in-energy'), { target: { value: '-5000' } })
    await press(pending, 'Review')
    expect(screen.getByTestId('frq-write-error').textContent).toContain('no sign')
    expect(screen.queryByTestId('frq-confirm-text')).toBeNull()
    expect(post).not.toHaveBeenCalled()
  })
})

describe('decisions', () => {
  it('grants as asked and replaces the row from the returned view', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: grantedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    expect(within(pending).getByTestId('frq-state').textContent).toBe('pending')
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toBe(PENDING_BASE + 'answer')
    expect(post.mock.calls[0][1]).toEqual({ decision: 'grant' })
    const row = (await rows())[0]
    await waitFor(() => expect(within(row).getByTestId('frq-state').textContent).toBe('granted'))
    expect(within(row).getByTestId('response-energy').textContent).toBe('7,777 Wh')
    expect(screen.getByTestId('frq-write-note').textContent).toContain('is now granted')
    expect(screen.queryByTestId('frq-action')).toBeNull()
  })

  it('accepts the view wrapped under view', async () => {
    mockReads()
    mockWrite({ ok: true, data: { persisted: true, view: grantedView() } })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    await waitFor(() => expect(within(pending).getByTestId('frq-state').textContent).toBe('granted'))
  })

  it('bounds the write with a timeout and a signal', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: grantedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    await waitFor(() => expect(post).toHaveBeenCalled())
    expect(post.mock.calls[0][2]).toMatchObject({ timeoutMs: 20_000 })
    expect(post.mock.calls[0][2]?.signal).toBeInstanceOf(AbortSignal)
  })

  it('grants adjusted with the typed interval and magnitudes, the sign left to the server', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: grantedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant adjusted')
    await fireEvent.input(screen.getByTestId('frq-in-start'), { target: { value: '2026-10-01T12:00:00Z' } })
    await fireEvent.input(screen.getByTestId('frq-in-duration'), { target: { value: '600' } })
    await fireEvent.input(screen.getByTestId('frq-in-energy'), { target: { value: '5000' } })
    await fireEvent.input(screen.getByTestId('frq-in-power'), { target: { value: '2000' } })
    await press(pending, 'Review')
    await press(pending, 'Confirm')
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toBe(PENDING_BASE + 'answer')
    expect(post.mock.calls[0][1]).toEqual({
      decision: 'grant',
      interval: { start: Date.parse('2026-10-01T12:00:00Z') / 1000, duration: 600 },
      energy: { value: 5000, multiplier: 0 },
      power: { value: 2000, multiplier: 0 },
    })
  })

  it('prefills the adjusted interval with the requested window and no energy or power', async () => {
    mockReads()
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant adjusted')
    expect((screen.getByTestId('frq-in-start') as HTMLInputElement).value).toBe(
      new Date(1790003600 * 1000).toISOString().replace('.000Z', 'Z'),
    )
    expect((screen.getByTestId('frq-in-duration') as HTMLInputElement).value).toBe('1800')
    expect((screen.getByTestId('frq-in-energy') as HTMLInputElement).value).toBe('')
    expect((screen.getByTestId('frq-in-power') as HTMLInputElement).value).toBe('')
  })

  it('denies and shows the denial the server returned', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: deniedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Deny')
    expect(screen.getByTestId('frq-confirm-text').textContent).toContain('Deny the request')
    await press(pending, 'Confirm')
    await waitFor(() => expect(within(pending).getByTestId('frq-state').textContent).toBe('denied'))
    expect(post.mock.calls[0][0]).toBe(PENDING_BASE + 'answer')
    expect(post.mock.calls[0][1]).toEqual({ decision: 'deny' })
  })

  it('revises a granted request with the reason and replaces the row', async () => {
    mockReads()
    const revised = copy().requests[1]
    revised.responses[1].energyAvailable = { value: -3000, multiplier: 0 }
    revised.tip = revised.responses[1]
    const post = mockWrite({ ok: true, data: revised })
    render(RequestQueuePane)
    const granted = (await rows())[1]
    await press(granted, 'Revise')
    expect((screen.getByTestId('frq-in-duration') as HTMLInputElement).value).toBe('1800')
    await fireEvent.input(screen.getByTestId('frq-in-energy'), { target: { value: '3000' } })
    await fireEvent.input(screen.getByTestId('frq-in-reason'), { target: { value: 'feeder limit' } })
    await press(granted, 'Review')
    await press(granted, 'Confirm')
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toBe('/api/derms/flow-reservations/4/frq-1789990000000000002/revise')
    expect(post.mock.calls[0][1]).toEqual({
      decision: 'grant',
      interval: { start: 1790000000, duration: 1800 },
      energy: { value: 3000, multiplier: 0 },
      reason: 'feeder limit',
    })
    await waitFor(() => expect(within(granted).getAllByTestId('response-energy')[0].textContent).toBe('-3,000 Wh'))
  })

  it('revises to a denial through the deny checkbox', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: copy().requests[1] })
    render(RequestQueuePane)
    const granted = (await rows())[1]
    await press(granted, 'Revise')
    await fireEvent.click(screen.getByTestId('frq-in-deny'))
    expect(screen.queryByTestId('frq-in-energy')).toBeNull()
    await press(granted, 'Review')
    await press(granted, 'Confirm')
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][1]).toEqual({ decision: 'deny' })
  })

  it('cancels a grant with an optional reason and shows the server view', async () => {
    mockReads()
    const cancelled = copy().requests[1]
    cancelled.state = 'cancelled'
    const post = mockWrite({ ok: true, data: cancelled })
    render(RequestQueuePane)
    const granted = (await rows())[1]
    await press(granted, 'Cancel grant')
    await fireEvent.input(screen.getByTestId('frq-in-reason'), { target: { value: 'fault' } })
    await press(granted, 'Review')
    expect(screen.getByTestId('frq-confirm-text').textContent).toContain('Cancel the grant and the controls')
    await press(granted, 'Confirm')
    await waitFor(() => expect(within(granted).getByTestId('frq-state').textContent).toBe('cancelled'))
    expect(post.mock.calls[0][0]).toBe('/api/derms/flow-reservations/4/frq-1789990000000000002/cancel')
    expect(post.mock.calls[0][1]).toEqual({ reason: 'fault' })
    expect(within(granted).queryAllByRole('button')).toHaveLength(0)
  })

  it('does not cancel when the operator backs out at the confirm step', async () => {
    mockReads()
    const post = mockWrite({ ok: true, data: copy().requests[1] })
    render(RequestQueuePane)
    const granted = (await rows())[1]
    await press(granted, 'Cancel grant')
    await press(granted, 'Review')
    await press(granted, 'Do not send')
    expect(post).not.toHaveBeenCalled()
    expect(within(granted).getByTestId('frq-state').textContent).toBe('granted')
  })
})

describe('refusals', () => {
  async function refused(res: Res) {
    mockReads()
    mockWrite(res)
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    const err = await screen.findByTestId('frq-write-error')
    return { pending, text: err.textContent ?? '' }
  }

  it.each([
    [400, 'interval_outside_window', 'outside the window the aggregator asked for', '', ''],
    [400, 'power_exceeds_request', 'more than the aggregator asked for', '', ''],
    [409, 'already_answered', 'already has an answer', 'TIPMRID01', 'frq-1790000000000000001'],
    [409, 'fleet_window_committed', 'overlaps a grant or control', 'CTRLMRID02', ''],
    [409, 'execution_exceeds_power', 'above the new power', 'CTRLMRID03', ''],
    [500, 'internal', 'failed to store', '', ''],
  ])('shows %i %s with its code, in words, naming %s %s', async (status, code, words, mRID, frqId) => {
    const { pending, text } = await refused({
      ok: false,
      status,
      error: 'server text',
      body: { error: 'server text', code, mRID, frqId },
    })
    expect(text).toContain(words)
    expect(text).toContain('(' + code + ')')
    if (mRID !== '') expect(text).toContain('mRID ' + mRID)
    if (frqId !== '') expect(text).toContain('request ' + frqId)
    expect(within(pending).getByTestId('frq-state').textContent).toBe('pending')
    expect(within(pending).getByRole('button', { name: 'Confirm' })).not.toBeDisabled()
  })

  it('shows a 503 not_configured as answers not enabled', async () => {
    const { text } = await refused({
      ok: false,
      status: 503,
      error: 'x',
      body: { error: 'x', code: 'not_configured' },
    })
    expect(text).toBe('Answers are not enabled on this server (not_configured).')
  })

  it.each([404, 405])('shows a %i with no refusal body as an API not available yet', async (status) => {
    const { text } = await refused({ ok: false, status, error: 'request failed with status ' + status })
    expect(text).toBe('Answer API not available on this server yet.')
  })

  it('shows a timeout as unknown outcome and re-reads the queue', async () => {
    const reads = mockReads()
    mockWrite({ ok: false, status: 0, error: 'request timed out' })
    render(RequestQueuePane)
    const [pending] = await rows()
    const before = reads.mock.calls.length
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    const err = await screen.findByTestId('frq-write-error')
    expect(err.textContent).toContain('may or may not have been applied')
    await waitFor(() => expect(reads.mock.calls.length).toBeGreaterThan(before))
    expect(within(pending).getByTestId('frq-state').textContent).toBe('pending')
  })

  it('says so when a success reply cannot be read, and re-reads the queue', async () => {
    const reads = mockReads()
    mockWrite({ ok: true, data: { nothing: 'useful' } })
    render(RequestQueuePane)
    const [pending] = await rows()
    const before = reads.mock.calls.length
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    const err = await screen.findByTestId('frq-write-error')
    expect(err.textContent).toContain('could not be read')
    await waitFor(() => expect(reads.mock.calls.length).toBeGreaterThan(before))
  })
})

describe('writes in flight', () => {
  function deferred() {
    let resolve!: (r: Res) => void
    const promise = new Promise<Res>((r) => (resolve = r))
    return { promise, resolve }
  }

  it('locks every action button and sends the write once while it is in flight', async () => {
    mockReads()
    const d = deferred()
    const post = mockWrite(d.promise)
    render(RequestQueuePane)
    const [pending, granted] = await rows()
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    expect(post).toHaveBeenCalledTimes(1)
    expect(within(pending).getByRole('button', { name: 'Confirm' })).toBeDisabled()
    expect(within(pending).getByRole('button', { name: 'Deny' })).toBeDisabled()
    expect(within(granted).getByRole('button', { name: 'Revise' })).toBeDisabled()
    expect(within(granted).getByRole('button', { name: 'Cancel grant' })).toBeDisabled()
    await fireEvent.click(within(pending).getByRole('button', { name: 'Confirm' }))
    expect(post).toHaveBeenCalledTimes(1)
    d.resolve({ ok: true, data: grantedView() })
    await waitFor(() => expect(within(pending).getByTestId('frq-state').textContent).toBe('granted'))
    expect(within(granted).getByRole('button', { name: 'Revise' })).not.toBeDisabled()
  })

  it('ignores an answer that lands after the operator stopped waiting', async () => {
    mockReads()
    const d = deferred()
    const post = mockWrite(d.promise)
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    const signal = post.mock.calls[0][2]?.signal as AbortSignal
    await press(pending, 'Stop waiting')
    expect(signal.aborted).toBe(true)
    expect(screen.getByTestId('frq-write-note').textContent).toContain('Stopped waiting')
    d.resolve({ ok: true, data: grantedView() })
    await new Promise((r) => setTimeout(r, 0))
    expect(within(pending).getByTestId('frq-state').textContent).toBe('pending')
    expect(screen.getByTestId('frq-write-note').textContent).toContain('Stopped waiting')
    expect(screen.queryByTestId('frq-action')).toBeNull()
  })

  it('aborts the in-flight write on unmount', async () => {
    mockReads()
    const d = deferred()
    const post = mockWrite(d.promise)
    const { unmount } = render(RequestQueuePane)
    const [pending] = await rows()
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    const signal = post.mock.calls[0][2]?.signal as AbortSignal
    unmount()
    expect(signal.aborted).toBe(true)
  })

  it('keeps the written row when a load that started before the write lands afterwards', async () => {
    let releaseLoad!: () => void
    const gate = new Promise<void>((r) => (releaseLoad = r))
    let flowCalls = 0
    vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
      if (path === '/api/derms/fleets') return { ok: true, data: [{ aggregatorLFDI: LFDI }] }
      flowCalls++
      if (flowCalls === 2) await gate
      return { ok: true, data: fixture }
    }) as never)
    mockWrite({ ok: true, data: grantedView() })
    render(RequestQueuePane)
    const [pending] = await rows()
    await press(document.body, 'Refresh')
    await press(pending, 'Grant as asked')
    await press(pending, 'Confirm')
    await waitFor(() => expect(within(pending).getByTestId('frq-state').textContent).toBe('granted'))
    releaseLoad()
    await new Promise((r) => setTimeout(r, 0))
    await waitFor(() => expect(flowCalls).toBe(2))
    const row = (await rows())[0]
    expect(within(row).getByTestId('frq-state').textContent).toBe('granted')
    expect(within(row).getByTestId('response-energy').textContent).toBe('7,777 Wh')
  })
})
