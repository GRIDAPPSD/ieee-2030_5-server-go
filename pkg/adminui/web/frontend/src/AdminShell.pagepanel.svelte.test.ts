// Issue 904: a panel list entry carrying `page` renders as a same-origin
// iframe filling the content area, and only for an authenticated shell.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import type { DashboardData } from './lib/dashboard'
import { isSameOriginPath, type PanelEntry } from './lib/descriptor'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

const data: DashboardData = {
  timestamp: '09:00:00',
  deviceCount: 0,
  mupCount: 0,
  tlsMode: 'TLS_AES_256_GCM_SHA384',
  uptime: '1m0s',
  commsOfflineAfterSeconds: 300,
  devices: [],
}

function mockServer(panels: PanelEntry[], dashboard: unknown = { ok: true, data }) {
  const spy = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path === '/dashboard/data') return dashboard as never
    if (path === '/api/ui/panels') return { ok: true, data: panels } as never
    return { ok: true, data: [] } as never
  })
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
  return spy
}

async function renderOn(path: string) {
  window.history.pushState({}, '', path)
  const result = render(AdminShell)
  await waitFor(() => expect(document.querySelector('.tab-bar') ?? screen.queryByText(/session required/i)).toBeTruthy())
  return result
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('AdminShell page panels', () => {
  it('renders a titled same-origin iframe on the entry page URL, with no descriptor request', async () => {
    const spy = mockServer([{ id: 'sim', label: 'Simulator', page: '/apps/sim/' }])
    await renderOn('/ui/overview')
    await fireEvent.click(await screen.findByTestId('tab-sim'))

    const frame = await screen.findByTestId('page-panel-sim')
    expect(frame.tagName).toBe('IFRAME')
    expect(frame).toHaveAttribute('src', '/apps/sim/')
    expect(frame).toHaveAttribute('title', 'Simulator')
    expect(screen.getByTestId('tab-sim')).toHaveAttribute('aria-current', 'page')
    expect(spy).not.toHaveBeenCalledWith('/api/ui/panels/sim', expect.anything())
  })

  it('renders the iframe on a hard load straight onto the panel path', async () => {
    mockServer([{ id: 'sim', label: 'Simulator', page: '/apps/sim/' }])
    await renderOn('/ui/sim')

    expect(await screen.findByTestId('page-panel-sim')).toHaveAttribute('src', '/apps/sim/')
    expect(screen.queryByTestId('not-found')).toBeNull()
  })

  it('creates no iframe on a core tab or for an entry without page', async () => {
    mockServer([
      { id: 'sim', label: 'Simulator', page: '/apps/sim/' },
      { id: 'reg', label: 'Registry' },
    ])
    await renderOn('/ui/overview')
    await screen.findByTestId('tab-sim')
    expect(document.querySelector('iframe')).toBeNull()

    await fireEvent.click(screen.getByTestId('tab-reg'))
    await waitFor(() => expect(screen.getByTestId('tab-reg')).toHaveAttribute('aria-current', 'page'))
    expect(document.querySelector('iframe')).toBeNull()
  })

  it('removes the iframe when the user leaves the tab', async () => {
    mockServer([{ id: 'sim', label: 'Simulator', page: '/apps/sim/' }])
    await renderOn('/ui/sim')
    await screen.findByTestId('page-panel-sim')

    await fireEvent.click(screen.getByTestId('tab-overview'))

    await waitFor(() => expect(document.querySelector('iframe')).toBeNull())
  })

  it('shows the login panel and creates no iframe when unauthenticated', async () => {
    const spy = mockServer([{ id: 'sim', label: 'Simulator', page: '/apps/sim/' }], {
      ok: false,
      status: 401,
      error: 'unauthorized',
    })
    await renderOn('/ui/sim')

    expect(screen.getByText(/session required/i)).toBeTruthy()
    expect(document.querySelector('iframe')).toBeNull()
    expect(spy).not.toHaveBeenCalledWith('/api/ui/panels', expect.anything())
  })

  it('refuses a page that is not a same-origin path rather than framing it', async () => {
    mockServer([
      { id: 'evil', label: 'Evil', page: 'https://example.com/' },
      { id: 'proto', label: 'Proto', page: '//example.com/x' },
    ])
    await renderOn('/ui/evil')

    expect(await screen.findByTestId('page-panel-error')).toBeTruthy()
    expect(document.querySelector('iframe')).toBeNull()
    await fireEvent.click(screen.getByTestId('tab-proto'))
    await waitFor(() => expect(screen.getByTestId('tab-proto')).toHaveAttribute('aria-current', 'page'))
    expect(document.querySelector('iframe')).toBeNull()
  })
})

describe('isSameOriginPath', () => {
  it.each(['/apps/sim/', '/apps/sim/?a=1#x', '/ui/overview'])('accepts %j', (page) => {
    expect(isSameOriginPath(page)).toBe(true)
  })

  // The URL parser strips tab, LF and CR anywhere in its input, so the
  // first three resolve to https://example.com/ despite starting with "/".
  it.each([
    ['tab', '/\t/example.com'],
    ['LF', '/\n/example.com'],
    ['CR', '/\r/example.com'],
    ['backslash', '/\\example.com'],
    ['double slash', '//example.com/x'],
    ['https', 'https://example.com/'],
    ['relative', 'apps/sim/'],
    ['space', '/apps/sim /'],
    ['NUL', '/apps/\u0000sim/'],
    ['empty', ''],
  ])('rejects %s', (_name, page) => {
    expect(isSameOriginPath(page)).toBe(false)
  })
})
