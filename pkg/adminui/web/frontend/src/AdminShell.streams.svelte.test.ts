// A stream panel inside the shell: the shell's tab and panel swaps are
// what close the stream the panel opened. AdminShell is rendered directly,
// as in AdminShell.panels.svelte.test.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import type { DashboardData } from './lib/dashboard'
import type { PanelEntry } from './lib/descriptor'
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

const panels: PanelEntry[] = [
  { id: 'feed-a', label: 'Feed A', stream: { maxLen: 8, charset: 'abc' } },
  { id: 'feed-b', label: 'Feed B', stream: { maxLen: 8, charset: 'abc' } },
]

class FakeEventSource {
  static instances: FakeEventSource[] = []
  static readonly CLOSED = 2
  onopen: (() => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  readyState = 0
  closed = false
  constructor(public url: string) {
    FakeEventSource.instances.push(this)
  }
  close() {
    this.closed = true
  }
}

beforeEach(() => {
  FakeEventSource.instances = []
  vi.stubGlobal('EventSource', FakeEventSource)
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path === '/dashboard/data') return { ok: true, data } as never
    if (path === '/api/ui/panels') return { ok: true, data: panels } as never
    if (path === '/api/fsas') return { ok: true, data: { fsas: [] } } as never
    return { ok: true, data: [] } as never
  })
  vi.spyOn(dash, 'connectDashboard').mockReturnValue(() => {})
})

afterEach(() => {
  vi.unstubAllGlobals()
})

async function openFeedA() {
  window.history.pushState({}, '', '/ui/feed-a')
  const view = render(AdminShell)
  await screen.findByTestId('stream-panel')
  await fireEvent.input(screen.getByLabelText('Parameter'), { target: { value: 'abc' } })
  await fireEvent.click(screen.getByRole('button', { name: 'Start' }))
  expect(FakeEventSource.instances).toHaveLength(1)
  return view
}

describe('AdminShell stream panels', () => {
  it('renders a panel that advertises a stream as a stream panel, not a descriptor read', async () => {
    window.history.pushState({}, '', '/ui/feed-a')
    const view = render(AdminShell)
    await screen.findByTestId('stream-panel')
    expect(screen.getByRole('heading', { name: 'Feed A' })).toBeInTheDocument()
    expect(api.fetchJSON).not.toHaveBeenCalledWith('/api/ui/panels/feed-a', expect.anything())
    view.unmount()
  })

  it('closes the stream when the viewer changes tab', async () => {
    const view = await openFeedA()
    await fireEvent.click(screen.getByTestId('tab-overview'))
    await waitFor(() => expect(screen.queryByTestId('stream-panel')).toBeNull())
    expect(FakeEventSource.instances[0].closed).toBe(true)
    view.unmount()
  })

  it('closes the stream when the viewer changes to another panel', async () => {
    const view = await openFeedA()
    await fireEvent.click(screen.getByTestId('tab-feed-b'))
    await waitFor(() => expect(window.location.pathname).toBe('/ui/feed-b'))
    expect(FakeEventSource.instances[0].closed).toBe(true)
    expect(FakeEventSource.instances).toHaveLength(1)
    view.unmount()
  })
})
