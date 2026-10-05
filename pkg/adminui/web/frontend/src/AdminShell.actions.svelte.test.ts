// A panel that advertises actions gets its forms beside its view; a panel
// that does not gets no action request at all.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import type { PanelEntry } from './lib/descriptor'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

const schema = { actions: [{ id: 'go', label: 'Go', fields: [{ name: 'n', label: 'N', kind: 'integer', min: 1, max: 3 }] }] }

function serve(panels: PanelEntry[]) {
  const spy = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path === '/dashboard/data') {
      return { ok: true, data: { timestamp: '09:00:00', deviceCount: 0, mupCount: 0, tlsMode: 'x', uptime: '1m', devices: [] } } as never
    }
    if (path === '/api/ui/panels') return { ok: true, data: panels } as never
    if (path === '/api/ui/panels/bridge/actions') return { ok: true, data: schema } as never
    if (path.startsWith('/api/ui/panels/')) return { ok: true, data: { version: 2, sections: [] } } as never
    return { ok: true, data: [] } as never
  })
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
  return spy
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('AdminShell panel actions', () => {
  it('renders the action forms for a panel that advertises actions', async () => {
    const spy = serve([{ id: 'bridge', label: 'Bridge', actions: [{ id: 'go', label: 'Go' }] }])
    window.history.pushState({}, '', '/ui/bridge')
    const view = render(AdminShell)
    expect(await screen.findByTestId('action-go')).toBeInTheDocument()
    expect(spy).toHaveBeenCalledWith('/api/ui/panels/bridge/actions', expect.anything())
    view.unmount()
  })

  it('reads no action schema for a panel that advertises none', async () => {
    const spy = serve([{ id: 'plain', label: 'Plain' }])
    window.history.pushState({}, '', '/ui/plain')
    const view = render(AdminShell)
    await waitFor(() => expect(spy).toHaveBeenCalledWith('/api/ui/panels/plain', expect.anything()))
    expect(screen.queryByTestId('actions-panel')).toBeNull()
    expect(spy).not.toHaveBeenCalledWith('/api/ui/panels/plain/actions', expect.anything())
    view.unmount()
  })
})
