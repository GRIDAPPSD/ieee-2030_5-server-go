// Step A of issue 561: the tab bar and the per-tab card partition
// (criteria 1, 2 and 3). AdminShell is rendered directly, not through
// App, since it now reads the active tab from currentPath itself; the
// component-identity guarantee that a route change does not remount it is
// App.svelte.test.ts's concern, not this file's.
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
  deviceCount: 1,
  mupCount: 0,
  tlsMode: 'TLS_AES_256_GCM_SHA384',
  uptime: '1m0s',
  devices: [
    { sfdi: '167261211635', lfdi: '3E4F45AB31EDFE5B67E343E5E4562E31984E23E5', href: '/edev/1', enabled: true },
  ],
}

function mockAuthenticated() {
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path === '/dashboard/data') return { ok: true, data } as never
    if (path === '/api/fsas') return { ok: true, data: { fsas: [] } } as never
    return { ok: true, data: { kind: 'SY', id: 'sy', label: 'System' } } as never
  })
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
}

async function renderOnPath(path: string) {
  window.history.pushState({}, '', path)
  mockAuthenticated()
  const result = render(AdminShell)
  await waitFor(() => expect(result.container.querySelector('.grid h2')).toBeTruthy())
  return result
}

// The tab-to-card partition from issue 561's Context section, read as the
// rendered heading text. TopologyTree's own heading carries the
// "SY -> FD -> SP -> DEV" parenthetical, not the issue's shorter card
// name "FSA Tree"; the assertion below matches the rendered DOM text, so
// that mismatch does not make the FSAs case a false failure.
const TAB_HEADINGS: Record<string, string[]> = {
  overview: ['Connected Devices', 'Server Info', 'Device Activity'],
  devices: ['End Devices', 'Add End Device', 'Lookup Device by LFDI'],
  fsas: ['Create FSA Template', 'FSA Templates', 'FSA Tree (SY -> FD -> SP -> DEV)'],
  control: ['Send DER Control'],
  certificates: ['Certificate Management'],
}

describe('AdminShell tab card partition (criterion 1)', () => {
  for (const [slug, expected] of Object.entries(TAB_HEADINGS)) {
    const path = slug === 'overview' ? '/' : `/ui/${slug}`

    it(`renders exactly the ${slug} tab's cards`, async () => {
      const { container } = await renderOnPath(path)
      const headings = Array.from(container.querySelectorAll('.grid h2')).map((h) => h.textContent)
      // A sorted-array equality is a set equality here: a card duplicated
      // onto a second tab or dropped from every tab changes the array's
      // length or membership, either of which fails toEqual regardless of
      // rendering order.
      expect(headings.slice().sort()).toEqual(expected.slice().sort())
    })
  }
})

describe('AdminShell threads its shell state to the tab that owns it', () => {
  it('shows the fetched device row on the devices tab and nowhere else', async () => {
    const { container } = await renderOnPath('/ui/devices')
    await waitFor(() => {
      expect(container.querySelector('[data-testid="device-sfdi"]')).toHaveTextContent('167261211635')
    })

    router.navigate('/')
    await tick()

    expect(container.querySelector('[data-testid="device-sfdi"]')).toBeNull()
  })
})

describe('AdminShell default route (criterion 2)', () => {
  it('renders the overview tab at "/" and does not rewrite the address bar', async () => {
    await renderOnPath('/')
    expect(screen.getByTestId('tab-overview')).toHaveAttribute('aria-current', 'page')
    expect(window.location.pathname).toBe('/')
  })

  it('renders the overview tab at "/ui/" and does not rewrite the address bar', async () => {
    await renderOnPath('/ui/')
    expect(screen.getByTestId('tab-overview')).toHaveAttribute('aria-current', 'page')
    expect(window.location.pathname).toBe('/ui/')
  })
})

describe('AdminShell tab bar (criterion 3)', () => {
  it('sits outside .navbar, below the header, with one correctly-addressed anchor per tab', async () => {
    const { container } = await renderOnPath('/')

    const tabBar = container.querySelector('.tab-bar')
    expect(tabBar?.tagName).toBe('NAV')
    expect(container.querySelector('.navbar .tab-bar')).toBeNull()

    for (const slug of Object.keys(TAB_HEADINGS)) {
      const anchor = screen.getByTestId(`tab-${slug}`)
      expect(anchor).toHaveAttribute('href', `/ui/${slug}`)
    }
  })

  it('marks only the active tab as aria-current after a client-side navigation', async () => {
    await renderOnPath('/')

    router.navigate('/ui/devices')
    await tick()

    expect(screen.getByTestId('tab-devices')).toHaveAttribute('aria-current', 'page')
    expect(screen.getByTestId('tab-overview')).not.toHaveAttribute('aria-current')
    expect(screen.getByTestId('tab-fsas')).not.toHaveAttribute('aria-current')
  })

  it('navigates client-side on a plain click, with no page load', async () => {
    await renderOnPath('/')
    const navigate = vi.spyOn(router, 'navigate')

    const anchor = screen.getByTestId('tab-devices')
    // @testing-library/svelte's fireEvent awaits a tick before resolving;
    // the boolean it resolves to is dispatchEvent's own return value,
    // false exactly when preventDefault was called. The click handler
    // must be the thing that stops the browser's page load, not an
    // absence of a href.
    const notCancelled = await fireEvent.click(anchor)

    expect(notCancelled).toBe(false)
    expect(navigate).toHaveBeenCalledWith('/ui/devices')
    expect(screen.getByTestId('tab-devices')).toHaveAttribute('aria-current', 'page')
  })

  it('keeps the browser default on a ctrl-click and does not navigate client-side', async () => {
    await renderOnPath('/')
    const navigate = vi.spyOn(router, 'navigate')

    const anchor = screen.getByTestId('tab-devices')
    const notCancelled = await fireEvent.click(anchor, { ctrlKey: true })

    expect(notCancelled).toBe(true)
    expect(navigate).not.toHaveBeenCalled()
  })

  it('keeps the browser default on a middle click and does not navigate client-side', async () => {
    await renderOnPath('/')
    const navigate = vi.spyOn(router, 'navigate')

    const anchor = screen.getByTestId('tab-devices')
    const notCancelled = await fireEvent.click(anchor, { button: 1 })

    expect(notCancelled).toBe(true)
    expect(navigate).not.toHaveBeenCalled()
  })
})
