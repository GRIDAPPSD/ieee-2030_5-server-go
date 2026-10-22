// The Logs drawer reads GET /api/device-logs/{lfdi} through fetchJSON, so the
// tests answer by path and assert the request the server would see. Timers
// are faked: the 5 s poll is advanced by hand, and the teardown cases count
// requests after the drawer is gone.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/svelte'
import DeviceLog from './DeviceLog.svelte'
import * as api from '../lib/api'
import { loadDeviceProfiles, resetDeviceProfiles } from '../lib/devices'

const LFDI = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
const MANAGER = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB'
const DEVICES = [
  { lfdi: LFDI, sfdi: '10000000001' },
  { lfdi: MANAGER, sfdi: '10000000002' },
]

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  resetDeviceProfiles()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

interface Reply {
  ok: boolean
  status?: number
  error?: string
  data?: unknown
}

function entry(n: number, line: Record<string, unknown> = {}, source?: string) {
  return {
    cursor: `c${n}`,
    ...(source ? { source } : {}),
    line: {
      seq: n,
      t: new Date(Date.UTC(2026, 9, 22, 12, 0, n)).toISOString(),
      kind: 'request',
      actor: LFDI,
      method: 'GET',
      path: `/dcap/${n}`,
      status: 200,
      dur_ms: 1.5,
      ...line,
    },
  }
}

function page(entries: unknown[], over: Record<string, unknown> = {}) {
  const list = entries as { cursor: string }[]
  return {
    ok: true,
    data: {
      lfdi: LFDI,
      entries,
      nextBefore: null,
      nextAfter: list.length ? list[0].cursor : null,
      rotatedRead: false,
      dropped: 0,
      ...over,
    },
  }
}

function serve(answer: (path: string) => Reply | Promise<Reply>) {
  const paths: string[] = []
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    paths.push(path)
    return (await answer(path)) as never
  })
  return paths
}

function mount(onClose = () => {}) {
  return render(DeviceLog, { props: { deviceId: '7', lfdi: LFDI, devices: DEVICES, onClose } })
}

async function settle() {
  await vi.advanceTimersByTimeAsync(0)
}

describe('DeviceLog', () => {
  it('shows each line newest first with its status, duration and detail', async () => {
    serve(() =>
      page([
        entry(3, { kind: 'logevent', method: undefined, path: undefined, status: undefined, functionSet: 9, logEventCode: 4, details: 'low battery' }),
        entry(2, { status: 404, path: '/dcap/missing' }),
        entry(1),
      ]),
    )
    mount()
    await settle()
    const rows = screen.getAllByTestId('device-log-row')
    expect(rows).toHaveLength(3)
    expect(within(rows[0]).getByText('logevent')).toBeInTheDocument()
    expect(rows[0]).toHaveTextContent('function set 9, code 4, low battery')
    expect(rows[1]).toHaveTextContent('GET /dcap/missing')
    expect(within(rows[1]).getByTestId('device-log-status')).toHaveTextContent('404')
    expect(within(rows[1]).getByTestId('device-log-status')).toHaveClass('offline')
    expect(within(rows[2]).getByTestId('device-log-status')).not.toHaveClass('offline')
    expect(rows[2]).toHaveTextContent('1.5')
  })

  it('labels a line made by another actor with via and that device label', async () => {
    serve(() => page([entry(2, { actor: MANAGER }), entry(1)]))
    mount()
    await settle()
    const rows = screen.getAllByTestId('device-log-row')
    expect(within(rows[0]).getByTestId('device-log-via')).toHaveTextContent('via 10000000002')
    expect(within(rows[1]).getByTestId('device-log-via').textContent?.trim()).toBe('')
  })

  it('renders line fields as text, never markup', async () => {
    serve(() => page([entry(1, { path: '/x<img src=x onerror=alert(1)>', details: '<b>bold</b>', logEventCode: 1 })]))
    const { container } = mount()
    await settle()
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('b')).toBeNull()
    expect(screen.getByTestId('device-log-row')).toHaveTextContent('<img src=x onerror=alert(1)>')
  })

  it('polls with the newest cursor every 5 s and adds only new lines', async () => {
    let tick = 0
    const paths = serve((path) => {
      if (!path.includes('after=')) return page([entry(2), entry(1)])
      tick++
      // The second poll repeats a line the first one already delivered.
      return tick === 1 ? page([entry(3)]) : page([entry(4), entry(3)])
    })
    mount()
    await settle()
    expect(paths).toEqual([`/api/device-logs/${LFDI}?limit=200`])

    await vi.advanceTimersByTimeAsync(5000)
    expect(paths[1]).toBe(`/api/device-logs/${LFDI}?limit=200&after=c2`)
    expect(screen.getAllByTestId('device-log-row')).toHaveLength(3)

    await vi.advanceTimersByTimeAsync(5000)
    expect(paths[2]).toBe(`/api/device-logs/${LFDI}?limit=200&after=c3`)
    const paths4 = screen.getAllByTestId('device-log-row').map((r) => r.textContent)
    expect(paths4).toHaveLength(4)
    expect(paths4[0]).toContain('/dcap/4')
    expect(paths4[3]).toContain('/dcap/1')
  })

  it('stops polling and aborts the request in flight when the drawer unmounts', async () => {
    const signals: (AbortSignal | undefined)[] = []
    const paths: string[] = []
    vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string, opts) => {
      paths.push(path)
      signals.push(opts?.signal)
      return page([entry(1)]) as never
    })
    const view = mount()
    await settle()
    await vi.advanceTimersByTimeAsync(5000)
    expect(paths).toHaveLength(2)

    view.unmount()
    expect(signals.every((s) => s?.aborted)).toBe(true)
    await vi.advanceTimersByTimeAsync(30000)
    expect(paths).toHaveLength(2)
  })

  it('pages back with the before cursor and drops Older when none remain', async () => {
    const paths = serve((path) =>
      path.includes('before=')
        ? page([entry(2), entry(1)], { nextBefore: null, nextAfter: 'c2' })
        : page([entry(4), entry(3)], { nextBefore: 'c3' }),
    )
    mount()
    await settle()
    await fireEvent.click(screen.getByTestId('device-logs-older'))
    await settle()
    expect(paths[1]).toBe(`/api/device-logs/${LFDI}?limit=200&before=c3`)
    expect(screen.getAllByTestId('device-log-row')).toHaveLength(4)
    expect(screen.queryByTestId('device-logs-older')).toBeNull()
  })

  it('says there is no log yet on 404 and shows the server reason on 503', async () => {
    serve(() => ({ ok: false, status: 404, error: 'no log for this device' }))
    const first = mount()
    await settle()
    expect(screen.getByTestId('device-logs-error')).toHaveTextContent('No log yet for this device.')
    first.unmount()

    serve(() => ({ ok: false, status: 503, error: 'device log disabled' }))
    mount()
    await settle()
    expect(screen.getByTestId('device-logs-error')).toHaveTextContent('device log disabled')
  })

  it('keeps polling after a 404 and shows lines when the log appears', async () => {
    let calls = 0
    serve(() => (++calls === 1 ? { ok: false, status: 404, error: 'none' } : page([entry(1)])))
    mount()
    await settle()
    expect(screen.queryByTestId('device-log-row')).toBeNull()
    await vi.advanceTimersByTimeAsync(5000)
    expect(screen.getAllByTestId('device-log-row')).toHaveLength(1)
    expect(screen.queryByTestId('device-logs-error')).toBeNull()
  })

  it('points Download at the download route', async () => {
    serve(() => page([]))
    mount()
    await settle()
    expect(screen.getByTestId('device-logs-download')).toHaveAttribute('href', `/api/device-logs/${LFDI}/download`)
  })

  it('filters the loaded lines by kind and by errors only', async () => {
    serve(() =>
      page([
        entry(3, { kind: 'refused', status: 403, reason: 'no' }),
        entry(2, { kind: 'logevent', status: undefined }),
        entry(1),
      ]),
    )
    mount()
    await settle()
    await fireEvent.change(screen.getByTestId('device-logs-kind'), { target: { value: 'logevent' } })
    expect(screen.getAllByTestId('device-log-row')).toHaveLength(1)
    await fireEvent.change(screen.getByTestId('device-logs-kind'), { target: { value: 'all' } })
    await fireEvent.click(screen.getByTestId('device-logs-errors'))
    const rows = screen.getAllByTestId('device-log-row')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('403')
  })
  it('runs one poll at a time after the device changes under an open drawer', async () => {
    const other = 'CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC'
    const releases: (() => void)[] = []
    const paths: string[] = []
    vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      paths.push(path)
      await new Promise<void>((r) => releases.push(r))
      return page([entry(1)]) as never
    })
    const view = mount()
    await settle()
    await view.rerender({ deviceId: '8', lfdi: other, devices: DEVICES, onClose: () => {} })
    await settle()
    // The old device's reply lands while the new device's first read is out.
    releases[0]()
    await settle()
    await vi.advanceTimersByTimeAsync(5000)
    await vi.advanceTimersByTimeAsync(5000)
    const forOther = paths.filter((p) => p.includes(other))
    expect(forOther).toHaveLength(1)
    releases.forEach((r) => r())
  })

  it('keeps the older lines reachable when the first page was empty and a full page arrives', async () => {
    let calls = 0
    const full = Array.from({ length: 200 }, (_, i) => entry(400 - i))
    const paths = serve((path) => {
      calls++
      if (calls === 1) return page([])
      if (path.includes('before=')) return page([entry(1)], { nextBefore: null })
      if (path.includes('after=')) return page([])
      return page(full, { nextBefore: 'c201' })
    })
    mount()
    await settle()
    await vi.advanceTimersByTimeAsync(5000)
    expect(screen.getAllByTestId('device-log-row')).toHaveLength(200)
    await fireEvent.click(screen.getByTestId('device-logs-older'))
    await settle()
    expect(paths.at(-1)).toBe(`/api/device-logs/${LFDI}?limit=200&before=c201`)
    expect(screen.getAllByTestId('device-log-row')).toHaveLength(201)
  })

  it('gives the table a caption naming the device', async () => {
    serve(() => page([entry(1)]))
    mount()
    await settle()
    expect(screen.getByRole('table', { name: 'Log of device 7' })).toBeInTheDocument()
  })
  async function asAggregator() {
    vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({
      ok: true,
      data: { profiles: [{ lfdi: LFDI, name: 'Agg', kind: 'aggregator' }] },
    } as never)
    await loadDeviceProfiles()
  }

  it('offers the own and managed switch only for an aggregator', async () => {
    serve(() => page([entry(1)]))
    const plain = mount()
    await settle()
    expect(screen.queryByTestId('device-logs-scope')).toBeNull()
    plain.unmount()
    vi.restoreAllMocks()

    await asAggregator()
    serve(() => page([entry(1)]))
    mount()
    await settle()
    expect(screen.getByTestId('device-logs-scope')).toBeInTheDocument()
  })

  it('switches scope to managed, reloads with it, and shows the device of each line', async () => {
    await asAggregator()
    const paths = serve((path) =>
      path.includes('scope=managed')
        ? page([entry(3, { actor: MANAGER, path: '/theirs' }), entry(2, { actor: LFDI, path: '/mine' }, 'unregistered')])
        : page([entry(1, { path: '/mine-only' })]),
    )
    mount()
    await settle()
    expect(paths[0]).toBe(`/api/device-logs/${LFDI}?limit=200`)
    expect(screen.queryByRole('columnheader', { name: 'Device' })).toBeNull()

    await fireEvent.click(screen.getByLabelText('Own and managed devices'))
    await settle()
    expect(paths[1]).toBe(`/api/device-logs/${LFDI}?limit=200&scope=managed`)
    const rows = screen.getAllByTestId('device-log-row')
    expect(rows).toHaveLength(2)
    expect(screen.getByRole('columnheader', { name: 'Device' })).toBeInTheDocument()
    expect(within(rows[0]).getByTestId('device-log-device')).toHaveTextContent('10000000002')
    expect(within(rows[1]).getByTestId('device-log-device')).toHaveTextContent('Agg')
    expect(within(rows[1]).getByTestId('device-log-device')).toHaveTextContent('Agg (unregistered)')
    expect(within(rows[0]).getByTestId('device-log-device')).not.toHaveTextContent('unregistered')
    expect(within(rows[0]).getByTestId('device-log-via').textContent?.trim() ?? '').toBe('')

    await vi.advanceTimersByTimeAsync(5000)
    expect(paths[2]).toBe(`/api/device-logs/${LFDI}?limit=200&scope=managed&after=c3`)

    await fireEvent.click(screen.getByLabelText('Own requests'))
    await settle()
    expect(paths.at(-1)).toBe(`/api/device-logs/${LFDI}?limit=200`)
    expect(screen.getAllByTestId('device-log-row')).toHaveLength(1)
    expect(screen.queryByRole('columnheader', { name: 'Device' })).toBeNull()
  })
  it('drops a managed-scope reply that lands after the drawer switched back to own', async () => {
    await asAggregator()
    let release: () => void = () => {}
    serve(async (path) => {
      if (!path.includes('scope=managed')) return page([entry(1, { path: '/mine-only' })])
      await new Promise<void>((r) => (release = r))
      return page([entry(9, { actor: MANAGER, path: '/theirs' })])
    })
    mount()
    await settle()
    await fireEvent.click(screen.getByLabelText('Own and managed devices'))
    await settle()
    await fireEvent.click(screen.getByLabelText('Own requests'))
    await settle()
    release()
    await settle()
    const rows = screen.getAllByTestId('device-log-row')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('/mine-only')
    expect(screen.queryByRole('columnheader', { name: 'Device' })).toBeNull()
  })
})
