// Issue 829: the shell adds one nav entry per panel an embedder registered
// and renders the panel it routes to. AdminShell is rendered directly, as
// in AdminShell.tabs.svelte.test.ts.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import type { DashboardData } from './lib/dashboard'
import type { Descriptor, PanelEntry } from './lib/descriptor'
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

const CORE_TABS = ['Overview', 'Devices', 'FSAs', 'Control', 'Certificates', 'DERMS']

const registry: Descriptor = {
  version: 2,
  sections: [
    {
      kind: 'table',
      heading: 'Registry',
      prose: [],
      empty: 'No registry entries yet.',
      body: { columns: ['Name'], rows: [[{ kind: 'text', text: 'pv-1' }]] },
    },
  ],
}

type PanelsReply = { ok: true; data: unknown } | { ok: false; error: string; status: number }

function mockServer(panels: PanelsReply, panel: PanelsReply = { ok: true, data: registry }) {
  const spy = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path === '/dashboard/data') return { ok: true, data } as never
    if (path === '/api/ui/panels') return panels as never
    if (path.startsWith('/api/ui/panels/')) return panel as never
    if (path === '/api/fsas') return { ok: true, data: { fsas: [] } } as never
    return { ok: true, data: [] } as never
  })
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
  return spy
}

const entries = (...e: PanelEntry[]): PanelsReply => ({ ok: true, data: e })

function tabLabels(): (string | null)[] {
  return [...document.querySelectorAll('.tab-bar a')].map((a) => a.textContent)
}

async function renderOn(path: string) {
  window.history.pushState({}, '', path)
  const result = render(AdminShell)
  await waitFor(() => expect(document.querySelector('.tab-bar')).toBeTruthy())
  return result
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('AdminShell registered panels', () => {
  it('leaves the nav as the core tabs, in order, when no panel is registered', async () => {
    const spy = mockServer(entries())
    await renderOn('/ui/overview')
    await waitFor(() => expect(spy).toHaveBeenCalledWith('/api/ui/panels', expect.anything()))

    expect(tabLabels()).toEqual(CORE_TABS)
    expect(screen.queryByTestId('panels-error')).toBeNull()
  })

  it('adds one nav entry per registered panel after the core tabs, in the order served', async () => {
    mockServer(
      entries({ id: 'gridappsd-registry', label: 'Registry' }, { id: 'gridappsd-topology', label: 'Topology' }),
    )
    await renderOn('/ui/overview')

    await waitFor(() => expect(tabLabels()).toEqual([...CORE_TABS, 'Registry', 'Topology']))
    expect(screen.getByTestId('tab-gridappsd-registry')).toHaveAttribute('href', '/ui/gridappsd-registry')
  })

  it('renders the panel a nav entry routes to, from its own descriptor', async () => {
    const spy = mockServer(entries({ id: 'gridappsd-registry', label: 'Registry' }))
    await renderOn('/ui/overview')
    const tab = await screen.findByTestId('tab-gridappsd-registry')

    await fireEvent.click(tab)

    await screen.findByTestId('descriptor-section')
    expect(window.location.pathname).toBe('/ui/gridappsd-registry')
    expect(spy).toHaveBeenCalledWith('/api/ui/panels/gridappsd-registry', expect.anything())
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('Registry')
    expect(screen.getByTestId('tab-gridappsd-registry')).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByTestId('not-found')).toBeNull()
  })

  it('renders a hard load straight onto a panel path once the list arrives, without a not-found flash', async () => {
    mockServer(entries({ id: 'gridappsd-registry', label: 'Registry' }))
    await renderOn('/ui/gridappsd-registry')

    await screen.findByTestId('descriptor-section')
    expect(screen.queryByTestId('not-found')).toBeNull()
  })

  it('still shows not-found for a path that is neither a core tab nor a registered panel', async () => {
    mockServer(entries({ id: 'gridappsd-registry', label: 'Registry' }))
    await renderOn('/ui/nonesuch')

    await screen.findByTestId('not-found')
  })

  it('keeps the core tabs and their content when a panel is registered', async () => {
    mockServer(entries({ id: 'gridappsd-registry', label: 'Registry' }))
    await renderOn('/ui/control')

    await waitFor(() => expect(tabLabels()).toEqual([...CORE_TABS, 'Registry']))
    expect(screen.getByRole('heading', { name: 'Send DER Control' })).toBeInTheDocument()
    expect(screen.queryByTestId('descriptor-section')).toBeNull()
  })

  it('shows a failed panel read in words, not as an empty panel', async () => {
    mockServer(entries({ id: 'gridappsd-registry', label: 'Registry' }), {
      ok: false,
      status: 504,
      error: 'panel did not answer in time',
    })
    await renderOn('/ui/gridappsd-registry')

    const alert = await screen.findByTestId('panel-error')
    expect(alert).toHaveTextContent('This panel did not answer in time.')
    expect(screen.queryByTestId('descriptor-section')).toBeNull()
    expect(screen.queryByTestId('descriptor-empty')).toBeNull()
  })

  it('says when the list of registered tabs could not be read, and keeps the core tabs', async () => {
    mockServer({ ok: false, status: 500, error: 'boom' })
    await renderOn('/ui/overview')

    const alert = await screen.findByTestId('panels-error')
    expect(alert).toHaveTextContent('Could not load the registered tabs: boom')
    expect(tabLabels()).toEqual(CORE_TABS)
  })

  it('renders a hostile panel label and id as text', async () => {
    const hostile = '<img src=x onerror="window.__pwned=5">'
    mockServer(entries({ id: 'p', label: hostile }))
    await renderOn('/ui/overview')

    const tab = await screen.findByTestId('tab-p')
    expect(tab.textContent).toBe(hostile)
    expect(tab.querySelector('img')).toBeNull()
  })

  it('reads no panel list for an unauthenticated load', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, error: 'admin authentication required', status: 401 })
    window.history.pushState({}, '', '/ui/overview')
    render(AdminShell)

    await screen.findByTestId('login-panel')
    expect(spy.mock.calls.map((c) => c[0])).toEqual(['/dashboard/data'])
  })

  it('normalizes a trailing slash on a panel path the way core tabs do', async () => {
    mockServer(entries({ id: 'gridappsd-registry', label: 'Registry' }))
    await renderOn('/ui/gridappsd-registry/')

    await screen.findByTestId('descriptor-section')
    await waitFor(() => expect(window.location.pathname).toBe('/ui/gridappsd-registry'))
    expect(screen.queryByTestId('not-found')).toBeNull()
  })

  it('gives up on a panel list that never answers after 10s and says so', async () => {
    vi.useFakeTimers()
    try {
      vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
      vi.stubGlobal(
        'fetch',
        vi.fn((path: string, init?: RequestInit) => {
          if (path === '/dashboard/data') {
            return Promise.resolve(new Response(JSON.stringify(data), { status: 200 }))
          }
          return new Promise((_, reject) => {
            init?.signal?.addEventListener('abort', () => reject(new Error('aborted')))
          })
        }),
      )
      window.history.pushState({}, '', '/ui/overview')
      const { unmount } = render(AdminShell)
      await vi.advanceTimersByTimeAsync(9999)
      expect(screen.queryByTestId('panels-error')).toBeNull()
      await vi.advanceTimersByTimeAsync(1)

      expect(screen.getByTestId('panels-error')).toHaveTextContent('Could not load the registered tabs: request timed out')
      unmount()
    } finally {
      vi.unstubAllGlobals()
      vi.useRealTimers()
    }
  })
})
