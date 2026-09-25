// The SSE stream is authorized by a one-time ticket in the query string,
// so the assertion is on the URL the EventSource was opened with: a
// reconnect that reuses a spent ticket authenticates nothing. The
// exchange is mocked at api.postJSON, the client every caller goes
// through; the credential mode and decode behaviour of that client are
// asserted in api.test.ts rather than duplicated here.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from './api'
import { appendHistory, connectDashboard, HISTORY_LIMIT, type DashboardData } from './dashboard'

const sample = (timestamp: string, devices: number, mups: number): DashboardData => ({
  timestamp,
  deviceCount: devices,
  mupCount: mups,
  tlsMode: 'GCM',
  uptime: '1s',
  devices: [],
})

class FakeEventSource {
  static opened: string[] = []
  static last: FakeEventSource | null = null
  onmessage: ((e: MessageEvent<string>) => void) | null = null
  onerror: (() => void) | null = null
  closed = false

  constructor(url: string) {
    FakeEventSource.opened.push(url)
    FakeEventSource.last = this
  }

  close() {
    this.closed = true
  }
}

describe('appendHistory', () => {
  it('appends the sample values from the payload', () => {
    const history = appendHistory([], sample('00:00:05', 4, 2))
    expect(history).toEqual([{ time: '00:00:05', devices: 4, mups: 2 }])
  })

  it('trims to the history limit, dropping the oldest sample', () => {
    let history = appendHistory([], sample('first', 0, 0))
    for (let i = 1; i <= HISTORY_LIMIT; i++) {
      history = appendHistory(history, sample(`t${i}`, i, i))
    }

    expect(history).toHaveLength(HISTORY_LIMIT)
    expect(history[0].time).toBe('t1')
    expect(history[history.length - 1].time).toBe(`t${HISTORY_LIMIT}`)
  })
})

describe('connectDashboard', () => {
  beforeEach(() => {
    FakeEventSource.opened = []
    FakeEventSource.last = null
    vi.stubGlobal('EventSource', FakeEventSource)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('exchanges a ticket and opens the stream with it', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { ticket: 'tok en/1' },
    })

    const stop = connectDashboard(() => {})
    await vi.waitFor(() => expect(FakeEventSource.opened).toHaveLength(1))

    expect(post).toHaveBeenCalledWith('/auth/ticket', {})
    expect(FakeEventSource.opened[0]).toBe('/dashboard/events?ticket=tok%20en%2F1')
    stop()
  })

  it('still opens the stream without a ticket when the exchange fails', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: false,
      error: 'admin authentication required',
      status: 401,
    })

    const stop = connectDashboard(() => {})
    await vi.waitFor(() => expect(FakeEventSource.opened).toHaveLength(1))

    expect(FakeEventSource.opened[0]).toBe('/dashboard/events')
    stop()
  })

  it('opens the stream without a ticket when the exchange succeeds but carries none', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: {} })

    const stop = connectDashboard(() => {})
    await vi.waitFor(() => expect(FakeEventSource.opened).toHaveLength(1))

    expect(FakeEventSource.opened[0]).toBe('/dashboard/events')
    stop()
  })

  it('hands each parsed frame to the caller and discards an unparseable one', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { ticket: 't' } })
    const seen: DashboardData[] = []

    const stop = connectDashboard((d) => seen.push(d))
    await vi.waitFor(() => expect(FakeEventSource.last).not.toBeNull())

    const payload = sample('00:00:10', 9, 4)
    FakeEventSource.last!.onmessage!({ data: JSON.stringify(payload) } as MessageEvent<string>)
    FakeEventSource.last!.onmessage!({ data: 'not json' } as MessageEvent<string>)

    expect(seen).toEqual([payload])
    stop()
  })

  it('closes the stream when the caller disposes it', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { ticket: 't' } })

    const stop = connectDashboard(() => {})
    await vi.waitFor(() => expect(FakeEventSource.last).not.toBeNull())
    const opened = FakeEventSource.last!

    stop()
    expect(opened.closed).toBe(true)
  })
})
