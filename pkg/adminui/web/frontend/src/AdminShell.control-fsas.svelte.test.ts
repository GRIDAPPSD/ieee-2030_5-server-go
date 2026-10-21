// The Control tab's FSA target reads the FSA list the shell owns, so a hard
// load onto /ui/control and a return to the tab must both show the FSAs the
// server holds at that moment.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import * as router from './lib/router'
import type { DashboardData } from './lib/dashboard'
import type { AdminFSA } from './lib/fsa'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

afterEach(() => {
  vi.restoreAllMocks()
})

const data: DashboardData = {
  timestamp: '09:00:00',
  deviceCount: 0,
  mupCount: 0,
  tlsMode: 'TLS_AES_256_GCM_SHA384',
  uptime: '1m0s',
  commsOfflineAfterSeconds: 300,
  devices: [],
}

const fsa = (mRID: string, description: string, devices: string[]): AdminFSA => ({
  href: `/api/fsas/${mRID}`,
  mRID,
  description,
  primacy: 1,
  devices,
})

function mockServer(current: { fsas: AdminFSA[] }) {
  vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    if (path === '/dashboard/data') return { ok: true, data }
    if (path === '/api/fsas') return { ok: true, data: { fsas: current.fsas } }
    return { ok: true, data: { kind: 'SY', id: 'sy', label: 'System' } }
  }) as never)
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
}

const options = (c: HTMLElement) =>
  Array.from((c.querySelector('#controlFsa') as HTMLSelectElement).options).map((o) => o.textContent?.trim())

describe('FSAs on the Control tab', () => {
  it('a hard load onto the Control tab offers the FSAs', async () => {
    window.history.pushState({}, '', '/ui/control')
    mockServer({ fsas: [fsa('fsa-A', 'feeder A', ['d1', 'd2'])] })
    const { container } = render(AdminShell)
    await waitFor(() => expect(container.querySelector('#controlDevice')).toBeTruthy())
    await fireEvent.click(screen.getByTestId('control-target-fsa'))
    await waitFor(() => expect(options(container)).toContain('feeder A'))
    await fireEvent.change(container.querySelector('#controlFsa') as HTMLSelectElement, { target: { value: 'fsa-A' } })
    expect(await screen.findByTestId('fsa-control-reach')).toHaveTextContent('Reaches the 2 devices')
  })

  it('returning to the tab shows an FSA added meanwhile', async () => {
    window.history.pushState({}, '', '/ui/control')
    const current = { fsas: [fsa('fsa-A', 'feeder A', [])] }
    mockServer(current)
    const { container } = render(AdminShell)
    await waitFor(() => expect(container.querySelector('#controlDevice')).toBeTruthy())

    router.navigate('/ui/overview')
    await tick()
    current.fsas = [fsa('fsa-A', 'feeder A', []), fsa('fsa-B', 'feeder B', [])]
    router.navigate('/ui/control')
    await waitFor(() => expect(container.querySelector('#controlDevice')).toBeTruthy())
    await fireEvent.click(screen.getByTestId('control-target-fsa'))
    await waitFor(() => expect(options(container)).toContain('feeder B'))
  })
})
