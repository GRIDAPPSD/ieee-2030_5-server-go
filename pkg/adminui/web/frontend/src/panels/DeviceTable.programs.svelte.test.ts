// The Programs button of each device row opens that device's drawer under
// the row; a request left by a link on another tab opens one on mount, once.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import DeviceTable from './DeviceTable.svelte'
import * as api from '../lib/api'
import { requestPrograms, takeProgramsRequest } from '../lib/programsRequest'
import type { DashboardDevice } from '../lib/dashboard'

afterEach(() => {
  vi.restoreAllMocks()
  takeProgramsRequest()
})

function device(id: string): DashboardDevice {
  return {
    sfdi: `1000000000${id}`,
    lfdi: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
    href: `/edev/${id}`,
    enabled: true,
    lastRequest: null,
    comms: 'not_seen',
    lastKnown: false,
    ders: [],
  }
}

function mockReads() {
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path.startsWith('/api/der-programs')) {
      return { ok: true, data: { persists: true, total: 0, start: 0, limit: 500, programs: [] } } as never
    }
    return { ok: true, data: { templates: [] } } as never
  })
}

function mount() {
  return render(DeviceTable, { props: { devices: [device('3'), device('4')], fsas: [], onChanged: () => {} } })
}

describe('DeviceTable Programs drawer', () => {
  it('opens the drawer of the device whose button is pressed, and closes it on a second press', async () => {
    mockReads()
    mount()
    expect(screen.queryByTestId('device-programs-3')).toBeNull()

    await fireEvent.click(screen.getByTestId('device-programs-toggle-3'))
    expect(await screen.findByTestId('device-programs-3')).toBeInTheDocument()
    expect(screen.queryByTestId('device-programs-4')).toBeNull()
    expect(screen.getByTestId('device-programs-toggle-3')).toHaveAttribute('aria-expanded', 'true')

    await fireEvent.click(screen.getByTestId('device-programs-toggle-3'))
    expect(screen.queryByTestId('device-programs-3')).toBeNull()
  })

  it('moves the drawer when another device button is pressed', async () => {
    mockReads()
    mount()
    await fireEvent.click(screen.getByTestId('device-programs-toggle-3'))
    await fireEvent.click(screen.getByTestId('device-programs-toggle-4'))
    expect(await screen.findByTestId('device-programs-4')).toBeInTheDocument()
    expect(screen.queryByTestId('device-programs-3')).toBeNull()
  })

  it('opens the requested device on mount and consumes the request', async () => {
    mockReads()
    requestPrograms('4')
    const first = mount()
    expect(await screen.findByTestId('device-programs-4')).toBeInTheDocument()
    expect(takeProgramsRequest()).toBe('')

    first.unmount()
    mount()
    expect(screen.queryByTestId('device-programs-4')).toBeNull()
  })

  it('closes the drawer from its own Close button', async () => {
    mockReads()
    mount()
    await fireEvent.click(screen.getByTestId('device-programs-toggle-3'))
    await fireEvent.click(await screen.findByRole('button', { name: 'Close programs' }))
    expect(screen.queryByTestId('device-programs-3')).toBeNull()
  })
})

describe('DeviceTable Logs drawer', () => {
  it('opens the Logs drawer beside the Programs drawer and stops its reads when closed', async () => {
    vi.useFakeTimers()
    try {
      const paths: string[] = []
      vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
        paths.push(path)
        if (path.startsWith('/api/device-logs/')) {
          return { ok: true, data: { lfdi: '', entries: [], nextBefore: null, nextAfter: null, rotatedRead: false, dropped: 0 } } as never
        }
        return { ok: true, data: { templates: [], programs: [], persists: true, total: 0, start: 0, limit: 500 } } as never
      })
      mount()
      await fireEvent.click(screen.getByTestId('device-programs-toggle-3'))
      await fireEvent.click(screen.getByTestId('device-logs-toggle-3'))
      await vi.advanceTimersByTimeAsync(0)
      expect(screen.getByTestId('device-programs-3')).toBeInTheDocument()
      expect(screen.getByTestId('device-logs-3')).toBeInTheDocument()
      expect(screen.getByTestId('device-logs-toggle-3')).toHaveAttribute('aria-expanded', 'true')

      await fireEvent.click(screen.getByTestId('device-logs-toggle-4'))
      expect(screen.queryByTestId('device-logs-3')).toBeNull()
      expect(screen.getByTestId('device-logs-4')).toBeInTheDocument()

      await fireEvent.click(screen.getByTestId('device-logs-toggle-4'))
      const before = paths.filter((p) => p.startsWith('/api/device-logs/')).length
      await vi.advanceTimersByTimeAsync(20000)
      expect(paths.filter((p) => p.startsWith('/api/device-logs/')).length).toBe(before)
    } finally {
      vi.useRealTimers()
    }
  })
  it('links the Logs toggle to its open drawer and returns focus to it on Close', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({
      ok: true,
      data: { lfdi: '', entries: [], nextBefore: null, nextAfter: null, rotatedRead: false, dropped: 0 },
    } as never)
    mount()
    const toggle = screen.getByTestId('device-logs-toggle-3')
    expect(toggle).not.toHaveAttribute('aria-controls')
    await fireEvent.click(toggle)
    const drawer = await screen.findByTestId('device-logs-3')
    expect(toggle.getAttribute('aria-controls')).toBe(drawer.id)
    expect(drawer.id).not.toBe('')

    await fireEvent.click(screen.getByRole('button', { name: 'Close logs' }))
    await vi.waitFor(() => expect(toggle).toHaveFocus())
  })
})
