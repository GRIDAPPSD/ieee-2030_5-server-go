// Each device row links to the Logs tab with that device selected; the
// table itself no longer opens a log.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import DeviceTable from './DeviceTable.svelte'
import * as api from '../lib/api'
import type { DashboardDevice } from '../lib/dashboard'

const LFDI = 'ABCDEF0123456789ABCDEF0123456789ABCDEF01'

afterEach(() => {
  vi.restoreAllMocks()
  window.history.replaceState({}, '', '/')
})

function device(id: string, lfdi: string): DashboardDevice {
  return { sfdi: `1000000000${id}`, lfdi, href: `/edev/${id}`, enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: false, ders: [] }
}

describe('DeviceTable Logs link', () => {
  it('links each row to the Logs tab with its LFDI, and opens no drawer', async () => {
    const reads = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: {} } as never)
    window.history.replaceState({}, '', '/ui/devices')
    render(DeviceTable, { props: { devices: [device('3', LFDI), device('4', '')], fsas: [], onChanged: () => {} } })

    const link = screen.getByTestId('device-logs-link-3')
    expect(link).toHaveAttribute('href', `/ui/logs?device=${LFDI}`)
    expect(screen.queryByTestId('device-logs-link-4')).toBeNull()

    await fireEvent.click(link)
    expect(window.location.pathname + window.location.search).toBe(`/ui/logs?device=${LFDI}`)
    expect(screen.queryByTestId('device-logs')).toBeNull()
    expect(reads.mock.calls.filter(([p]) => String(p).startsWith('/api/device-logs/'))).toHaveLength(0)
  })
})
