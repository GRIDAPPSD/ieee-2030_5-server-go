// The Control tab's "no programs" note links to the Devices tab, and that
// link must land with the device's Programs drawer open. The shell stays
// mounted across the tab switch, so this runs the whole path.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import * as router from './lib/router'
import type { DashboardData } from './lib/dashboard'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

afterEach(() => {
  vi.restoreAllMocks()
})

const data: DashboardData = {
  timestamp: '09:00:00',
  deviceCount: 1,
  mupCount: 0,
  tlsMode: 'TLS_AES_256_GCM_SHA384',
  uptime: '1m0s',
  commsOfflineAfterSeconds: 300,
  devices: [
    { sfdi: '167261211635', lfdi: '3E4F45AB31EDFE5B67E343E5E4562E31984E23E5', href: '/edev/1', enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: true, ders: [] },
  ],
}

function mockServer() {
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path === '/dashboard/data') return { ok: true, data } as never
    if (path === '/api/fsas') return { ok: true, data: { fsas: [] } } as never
    if (path === '/api/devices/1/der-programs') return { ok: true, data: { device: '1', programs: [] } } as never
    if (path === '/api/der-programs?limit=500&start=0') {
      return { ok: true, data: { persists: true, total: 0, start: 0, limit: 500, programs: [] } } as never
    }
    return { ok: true, data: { kind: 'SY', id: 'sy', label: 'System' } } as never
  })
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
}

async function openControlNote() {
  window.history.pushState({}, '', '/ui/control')
  mockServer()
  const result = render(AdminShell)
  await waitFor(() => expect(result.container.querySelector('#controlDevice')).toBeTruthy())
  const select = result.container.querySelector('#controlDevice') as HTMLSelectElement
  await fireEvent.change(select, { target: { value: '1' } })
  return screen.findByTestId('der-control-devices-link')
}

describe('Devices tab link from the Control tab', () => {
  it('opens the chosen device Programs drawer on the Devices tab', async () => {
    const link = await openControlNote()
    await fireEvent.click(link)

    expect(window.location.pathname).toBe('/ui/devices')
    expect(await screen.findByTestId('device-programs-1')).toBeInTheDocument()
    expect(screen.getByTestId('device-programs-toggle-1')).toHaveAttribute('aria-expanded', 'true')
  })

  it('does not reopen the drawer on a later visit to the Devices tab', async () => {
    const link = await openControlNote()
    await fireEvent.click(link)
    await screen.findByTestId('device-programs-1')

    router.navigate('/ui/overview')
    await tick()
    router.navigate('/ui/devices')
    await screen.findByTestId('device-programs-toggle-1')
    expect(screen.queryByTestId('device-programs-1')).toBeNull()
  })
})
