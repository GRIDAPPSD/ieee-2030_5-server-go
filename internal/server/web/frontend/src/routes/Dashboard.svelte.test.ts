// The composition-level assertions: an unauthenticated load must show the
// login form instead of a page of empty panels, and an authenticated load
// must render the values the first authenticated read returned rather
// than waiting on the SSE stream for its first frame.
import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/svelte'
import Dashboard from './Dashboard.svelte'
import * as api from '../lib/api'
import * as dash from '../lib/dashboard'
import type { DashboardData } from '../lib/dashboard'
import { installCanvasStub } from '../test-canvas-stub'

installCanvasStub()

const data: DashboardData = {
  timestamp: '09:00:00',
  deviceCount: 2,
  mupCount: 5,
  tlsMode: 'TLS_AES_256_GCM_SHA384',
  uptime: '3m21s',
  devices: [
    { sfdi: '167261211635', lfdi: '3E4F45AB31EDFE5B67E343E5E4562E31984E23E5', href: '/edev/1', enabled: true },
  ],
}

describe('Dashboard', () => {
  it('shows the login form and opens no stream when the admin session is missing', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({
      ok: false,
      error: 'admin authentication required',
      status: 401,
    })
    const connect = vi.spyOn(dash, 'connectDashboard')

    render(Dashboard)

    await screen.findByTestId('login-panel')
    expect(connect).not.toHaveBeenCalled()
    expect(screen.queryByText('Send DER Control')).toBeNull()
  })

  it('renders the first authenticated payload values and opens the stream', async () => {
    vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      if (path === '/dashboard/data') return { ok: true, data } as never
      if (path === '/api/fsas') return { ok: true, data: { fsas: [] } } as never
      return { ok: true, data: { kind: 'SY', id: 'sy', label: 'System' } } as never
    })
    const connect = vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})

    const { container } = render(Dashboard)

    await waitFor(() => {
      expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('2')
    })
    expect(container.querySelector('#mupCount')).toHaveTextContent('5')
    expect(container.querySelector('#tlsMode')).toHaveTextContent('TLS_AES_256_GCM_SHA384')
    expect(container.querySelector('#uptime')).toHaveTextContent('3m21s')
    expect(screen.getAllByTestId('device-sfdi')[0]).toHaveTextContent('167261211635')
    expect(connect).toHaveBeenCalledTimes(1)
  })
})
