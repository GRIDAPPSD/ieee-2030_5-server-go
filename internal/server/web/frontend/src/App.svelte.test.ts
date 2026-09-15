// The shell is registered at both admin UI paths ("/" and "/ui/") in
// src/routes/index.ts, so this asserts the property that registration is
// meant to guarantee: a client-side navigation between them resolves to
// the same component reference, and Svelte's $derived component swap
// (App.svelte) does not destroy and recreate it. #560's shell hoist
// depends on this holding, since a swap would close the SSE stream.
import { describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import { render, waitFor } from '@testing-library/svelte'
import App from './App.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import type { DashboardData } from './lib/dashboard'
import { navigate } from './lib/router'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

const data: DashboardData = {
  timestamp: '09:00:00',
  deviceCount: 1,
  mupCount: 0,
  tlsMode: 'TLS_AES_256_GCM_SHA384',
  uptime: '1m0s',
  devices: [],
}

describe('App', () => {
  it('does not remount the shell or reopen its stream when the route changes between admin UI paths', async () => {
    window.history.pushState({}, '', '/')
    vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      if (path === '/dashboard/data') return { ok: true, data } as never
      if (path === '/api/fsas') return { ok: true, data: { fsas: [] } } as never
      return { ok: true, data: { kind: 'SY', id: 'sy', label: 'System' } } as never
    })
    const disconnect = vi.fn()
    const connect = vi.spyOn(dash, 'connectDashboard').mockReturnValue(disconnect)

    const { container } = render(App)

    await waitFor(() => {
      expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('1')
    })

    // A remount destroys the old instance in the same DOM update as
    // mounting the new one, and both onDestroy and the new onMount's
    // first await settle within Svelte's reactive flush, so a tick()
    // after each navigate is what a remount's extra connect/disconnect
    // calls would show up in.
    navigate('/ui/')
    await tick()
    // "/ui/" must resolve to the same shell content as "/", not to
    // whatever component the route table happens to map it to.
    expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('1')
    navigate('/')
    await tick()

    expect(connect).toHaveBeenCalledTimes(1)
    expect(disconnect).not.toHaveBeenCalled()
  })
})
