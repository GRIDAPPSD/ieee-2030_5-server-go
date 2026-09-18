// Issue 594, end to end: a mint on the Certificates tab must still be
// readable on the Devices tab after the tab switch, even though that
// switch unmounts and remounts both panels ({#if} in AdminShell.svelte;
// js-ts.md, "{#if} unmounts its block when the guard turns false"). The
// panel-level tests (CertPanel.svelte.test.ts, AddDevice.svelte.test.ts)
// cover the onMinted callback and the pending prop in isolation; this
// file is the only one that proves AdminShell's hoisted state actually
// carries the value across a real navigation.
import { describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import * as router from './lib/router'
import type { DashboardData } from './lib/dashboard'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

const data: DashboardData = {
  timestamp: '09:00:00',
  deviceCount: 0,
  mupCount: 0,
  tlsMode: 'TLS_AES_256_GCM_SHA384',
  uptime: '1m0s',
  devices: [],
}

function mockAuthenticated() {
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path === '/dashboard/data') return { ok: true, data } as never
    if (path === '/api/fsas') return { ok: true, data: { fsas: [] } } as never
    if (path === '/api/certs/device-types') {
      return { ok: true, data: { deviceTypes: [{ value: 1, name: 'generic', label: 'Generic' }] } } as never
    }
    return { ok: true, data: { kind: 'SY', id: 'sy', label: 'System' } } as never
  })
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
}

describe('AdminShell carries a mint across the Certificates -> Devices tab switch', () => {
  it('pre-fills Add End Device with the identifiers the mint on Certificates returned', async () => {
    window.history.pushState({}, '', '/ui/certificates')
    mockAuthenticated()
    vi.spyOn(api, 'postJSON').mockImplementation(async (path: string) => {
      if (path === '/api/certs/device') {
        return {
          ok: true,
          data: {
            certPEM: '-----BEGIN CERTIFICATE-----\nMIIBcarry\n-----END CERTIFICATE-----\n',
            keyPEM: '-----BEGIN EC PRIVATE KEY-----\nMHcCcarry\n-----END EC PRIVATE KEY-----\n',
            sfdi: '311635167262',
            lfdi: 'CARRIEDLFDI0000000000000000000000000001',
          },
        } as never
      }
      return { ok: true, data: {} } as never
    })

    const { container } = render(AdminShell)
    await waitFor(() => expect(container.querySelector('.grid h2')).toBeTruthy())
    await waitFor(() => expect(container.querySelector('#deviceType')).not.toBeDisabled())

    await fireEvent.input(container.querySelector('#hwSerial') as HTMLInputElement, {
      target: { value: 'PW-CARRY-001' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))
    await waitFor(() => {
      expect(container.querySelector('#certResult')).toHaveTextContent('Generated! SFDI: 311635167262')
    })

    router.navigate('/ui/devices')
    await tick()
    await waitFor(() => {
      expect(container.querySelector('.grid h2')?.textContent).not.toBe('Certificate Management')
    })

    expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe('311635167262')
    expect((container.querySelector('#addDevLFDI') as HTMLInputElement).value).toBe(
      'CARRIEDLFDI0000000000000000000000000001',
    )
  })
})
