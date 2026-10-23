// The Logs tab reads GET /api/device-logs/{lfdi} through fetchJSON, so the
// tests answer by path. The selection lives in the URL, which each test sets
// through window.history before mounting or reads back after a pick. Timers
// are faked so the 5 s poll is advanced by hand.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import LogsTab from './LogsTab.svelte'
import * as api from '../lib/api'
import { loadDeviceProfiles, resetDeviceProfiles } from '../lib/devices'
import type { DashboardDevice } from '../lib/dashboard'

const A = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
const B = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB'

function device(lfdi: string, sfdi: string): DashboardDevice {
  return { sfdi, lfdi, href: `/edev/${sfdi}`, enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: false, ders: [] }
}

const DEVICES = [device(A, '10000000001'), device(B, '10000000002')]

beforeEach(() => {
  vi.useFakeTimers()
  window.history.replaceState({}, '', '/ui/logs')
})

afterEach(() => {
  resetDeviceProfiles()
  vi.useRealTimers()
  vi.restoreAllMocks()
  window.history.replaceState({}, '', '/')
})

function line(lfdi: string, n: number, path: string) {
  return {
    cursor: `c${n}`,
    line: { seq: n, t: new Date(Date.UTC(2026, 9, 22, 12, 0, n)).toISOString(), kind: 'request', actor: lfdi, method: 'GET', path, status: 200, dur_ms: 1 },
  }
}

function logPage(lfdi: string, entries: { cursor: string }[]) {
  return {
    ok: true,
    data: { lfdi, entries, nextBefore: null, nextAfter: entries.length ? entries[0].cursor : null, rotatedRead: false, dropped: 0 },
  }
}

function serve(answer: (path: string, signal?: AbortSignal) => unknown | Promise<unknown>) {
  const calls: { path: string; signal?: AbortSignal }[] = []
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string, opts) => {
    calls.push({ path, signal: opts?.signal })
    return (await answer(path, opts?.signal)) as never
  })
  return calls
}

const logCalls = (calls: { path: string; signal?: AbortSignal }[]) => calls.filter((c) => c.path.startsWith('/api/device-logs/'))

function mount(ready = true, devices = DEVICES, error = '') {
  return render(LogsTab, { props: { devices, ready, error } })
}

async function settle() {
  await vi.advanceTimersByTimeAsync(0)
}

describe('LogsTab', () => {
  it('lists every device and aggregator in the picker by its label, as text', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({
      ok: true,
      data: { profiles: [{ lfdi: B, name: '<b>Agg</b>', kind: 'aggregator' }] },
    } as never)
    await loadDeviceProfiles()
    mount()
    const options = screen.getAllByRole('option').map((o) => o.textContent)
    expect(options).toEqual(['Pick a device', '10000000001', '<b>Agg</b>'])
    expect(screen.getByTestId('logs-picker').querySelector('b')).toBeNull()
    expect(screen.getByTestId('logs-pick-hint')).toBeInTheDocument()
  })

  it('opens the log of a picked device and puts the pick in the URL', async () => {
    const calls = serve((path) => logPage(A, [line(A, 1, '/dcap')]))
    mount()
    expect(logCalls(calls)).toHaveLength(0)

    await fireEvent.change(screen.getByTestId('logs-picker'), { target: { value: A } })
    await settle()
    expect(window.location.search).toBe(`?device=${A}`)
    expect(logCalls(calls).map((c) => c.path)).toEqual([`/api/device-logs/${A}?limit=200`])
    expect(screen.getByTestId('device-log-row')).toHaveTextContent('GET /dcap')
    expect(screen.getByTestId('logs-picker')).toHaveValue(A)
  })

  it('reopens the device named in the URL on load, whatever the case of its LFDI', async () => {
    window.history.replaceState({}, '', `/ui/logs?device=${B.toLowerCase()}`)
    const calls = serve(() => logPage(B, [line(B, 1, '/tm')]))
    mount()
    await settle()
    expect(logCalls(calls).map((c) => c.path)).toEqual([`/api/device-logs/${B}?limit=200`])
    expect(screen.getByTestId('device-log-row')).toHaveTextContent('GET /tm')
    expect(screen.getByTestId('logs-picker')).toHaveValue(B)
  })

  it('shows a plain not found for an unknown LFDI and reads no log', async () => {
    window.history.replaceState({}, '', '/ui/logs?device=DEADBEEF')
    const calls = serve(() => logPage(A, []))
    mount()
    await settle()
    expect(screen.getByTestId('logs-not-found')).toHaveTextContent('DEADBEEF')
    expect(screen.queryByTestId('device-logs')).toBeNull()
    expect(logCalls(calls)).toHaveLength(0)
  })

  it('does not report a device missing before the device list has loaded', async () => {
    window.history.replaceState({}, '', `/ui/logs?device=${A}`)
    const calls = serve(() => logPage(A, []))
    const view = mount(false, [])
    expect(screen.queryByTestId('logs-not-found')).toBeNull()
    await view.rerender({ devices: DEVICES, ready: true })
    await settle()
    expect(screen.queryByTestId('logs-not-found')).toBeNull()
    expect(logCalls(calls)).toHaveLength(1)
  })

  it('says the device list could not be read, not "not found", when the read failed', async () => {
    window.history.replaceState({}, '', `/ui/logs?device=${A}`)
    const calls = serve(() => logPage(A, []))
    mount(true, [], 'dashboard read failed')
    await settle()
    expect(screen.queryByTestId('logs-not-found')).toBeNull()
    expect(screen.getByTestId('logs-devices-error')).toHaveTextContent('dashboard read failed')
    expect(logCalls(calls)).toHaveLength(0)
  })

  it('aborts the first device and drops its late reply when another device is picked', async () => {
    let releaseA: () => void = () => {}
    const calls = serve(async (path) => {
      if (path.includes(A)) {
        await new Promise<void>((r) => (releaseA = r))
        return logPage(A, [line(A, 9, '/only-on-a')])
      }
      return logPage(B, [line(B, 1, '/only-on-b')])
    })
    mount()
    await fireEvent.change(screen.getByTestId('logs-picker'), { target: { value: A } })
    await settle()
    const first = calls[0].signal
    expect(first?.aborted).toBe(false)

    await fireEvent.change(screen.getByTestId('logs-picker'), { target: { value: B } })
    await settle()
    expect(first?.aborted).toBe(true)

    releaseA()
    await settle()
    const rows = screen.getAllByTestId('device-log-row')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('/only-on-b')
    expect(screen.queryByText('/only-on-a', { exact: false })).toBeNull()

    await vi.advanceTimersByTimeAsync(5000)
    const forA = logCalls(calls).filter((c) => c.path.includes(A))
    expect(forA).toHaveLength(1)
  })

  it('stops polling and aborts the request in flight when the tab is left', async () => {
    window.history.replaceState({}, '', `/ui/logs?device=${A}`)
    const calls = serve(() => logPage(A, [line(A, 1, '/dcap')]))
    const view = mount()
    await settle()
    await vi.advanceTimersByTimeAsync(5000)
    expect(logCalls(calls)).toHaveLength(2)

    view.unmount()
    expect(logCalls(calls).every((c) => c.signal?.aborted)).toBe(true)
    await vi.advanceTimersByTimeAsync(30000)
    expect(logCalls(calls)).toHaveLength(2)
  })

  it('keeps the log, its polling and its place when the device list refreshes with new objects for the same device', async () => {
    window.history.replaceState({}, '', `/ui/logs?device=${A}`)
    const calls = serve((path) =>
      path.includes('after=') ? logPage(A, [line(A, 2, '/second')]) : logPage(A, [line(A, 1, '/first')]),
    )
    const view = mount()
    await settle()
    const box = screen.getByTestId('device-logs-scroll')
    const row = screen.getByTestId('device-log-row')
    expect(logCalls(calls)).toHaveLength(1)

    // The dashboard stream hands over a fresh object for every device on each frame.
    await view.rerender({ devices: [device(A, '10000000001'), device(B, '10000000002')], ready: true, error: '' })
    await settle()
    expect(screen.getByTestId('device-logs-scroll')).toBe(box)
    expect(screen.getByTestId('device-log-row')).toBe(row)
    expect(logCalls(calls)).toHaveLength(1)
    expect(logCalls(calls)[0].signal?.aborted).toBe(false)
  })
})
