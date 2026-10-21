// The shell owns the device-name load (#1034): every tab entry and the
// device set trigger it, and the error it shows must come from the newest
// load only.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import * as router from './lib/router'
import { resetDeviceProfiles } from './lib/devices'
import type { DashboardData, DashboardDevice } from './lib/dashboard'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

const dev = (n: number): DashboardDevice => ({
  sfdi: `16726121163${n}`,
  lfdi: `3E4F45AB31EDFE5B67E343E5E4562E31984E23E${n}`,
  href: `/edev/${n}`,
  enabled: true,
  lastRequest: null,
  comms: 'not_seen',
  lastKnown: true,
  ders: [],
})

function dashboard(devices: DashboardDevice[]): DashboardData {
  return {
    timestamp: '09:00:00',
    deviceCount: devices.length,
    mupCount: 0,
    tlsMode: 'TLS_AES_256_GCM_SHA384',
    uptime: '1m0s',
    commsOfflineAfterSeconds: 300,
    devices,
  }
}

type ProfilesReply = (call: number) => unknown | Promise<unknown>

interface World {
  profileCalls: number
  push: (d: DashboardData) => void
}

// profiles answers each GET /api/device-profiles by call number.
function install(profiles: ProfilesReply): World {
  const world: World = { profileCalls: 0, push: () => {} }
  vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    if (path === '/dashboard/data') return { ok: true, data: dashboard([dev(1)]) }
    if (path === '/api/device-profiles') return profiles(++world.profileCalls)
    if (path === '/api/fsas') return { ok: true, data: { fsas: [] } }
    return { ok: true, data: { kind: 'SY', id: 'sy', label: 'System' } }
  }) as never)
  vi.spyOn(dash, 'connectDashboard').mockImplementation(((cb: (d: DashboardData) => void) => {
    world.push = cb
    return () => {}
  }) as never)
  return world
}

const ok = (list: unknown[] = []) => ({ ok: true, data: { profiles: list } })

afterEach(() => {
  vi.restoreAllMocks()
  resetDeviceProfiles()
})

describe('AdminShell device-name load', () => {
  it('keeps the error of the newest load when an older load succeeds late', async () => {
    let releaseFirst: (v: unknown) => void = () => {}
    const world = install((call) =>
      call === 1 ? new Promise((resolve) => (releaseFirst = resolve)) : { ok: false, error: 'down', status: 500 },
    )
    window.history.pushState({}, '', '/')
    render(AdminShell)
    await waitFor(() => expect(world.profileCalls).toBe(1))

    router.navigate('/ui/devices')
    await tick()
    expect(await screen.findByTestId('device-names-error')).toHaveTextContent('down')

    releaseFirst(ok())
    await tick()
    await tick()
    expect(screen.getByTestId('device-names-error')).toHaveTextContent('down')
  })

  it('does not reload on a device-set change; adding a device reloads once', async () => {
    const world = install(() => ok())
    vi.spyOn(api, 'postBody').mockResolvedValue({ ok: true, data: { sfdi: '311635167262', lfdi: 'AB'.repeat(20), subject: 'CN=x' } })
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { href: '/edev/2', sfdi: '311635167262', lfdi: 'AB'.repeat(20) } })
    window.history.pushState({}, '', '/ui/devices')
    const { container } = render(AdminShell)
    await waitFor(() => expect(world.profileCalls).toBe(1))
    await screen.findByRole('button', { name: 'Add Device' })

    await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, {
      target: { value: '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))
    await waitFor(() => expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe('311635167262'))
    await fireEvent.input(container.querySelector('#addDevPIN') as HTMLInputElement, { target: { value: '1234' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))
    await waitFor(() => expect(world.profileCalls).toBe(2))

    world.push(dashboard([dev(1), dev(2)]))
    await tick()
    await tick()
    expect(world.profileCalls).toBe(2)
  })
})
