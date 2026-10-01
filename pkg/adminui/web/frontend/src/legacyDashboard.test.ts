// The legacy dashboard (SEP2_ADMIN_LEGACY_DASHBOARD) is a string constant in
// internal/server/dashboard_html.go. This runs its script in jsdom against
// its own markup and feeds onSSEMessage a frame, so the rendered device table
// is what is asserted.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const source = readFileSync(resolve(__dirname, '../../../../../internal/server/dashboard_html.go'), 'utf8')
const body = source.slice(source.indexOf('<body'), source.indexOf('<script>'))
const script = source.slice(source.indexOf('<script>') + '<script>'.length, source.indexOf('</script>'))

type Frame = Record<string, unknown>
let onFrame: (event: { data: string }) => void

beforeEach(() => {
  document.body.innerHTML = body.replace(/^<body[^>]*>/, '')
  vi.stubGlobal('fetch', () => new Promise(() => {}))
  vi.stubGlobal('EventSource', class {})
  onFrame = new Function(script + '\nreturn onSSEMessage;')() as typeof onFrame
})

const frame = (extra: Frame) =>
  JSON.stringify({ timestamp: '09:00:00', deviceCount: 0, mupCount: 0, tlsMode: 'TLS', uptime: '1s', devices: null, ...extra })

describe('legacy dashboard device table', () => {
  it('shows the server error and not the empty-list row when the read failed', () => {
    onFrame({ data: frame({ error: 'device list unavailable: backend down' }) })
    const text = document.getElementById('deviceTable')?.textContent ?? ''
    expect(text).toBe('device list unavailable: backend down')
    expect(text).not.toContain('No devices registered')
  })

  it('still shows the empty-list row for a healthy empty server', () => {
    onFrame({ data: frame({ devices: null }) })
    expect(document.getElementById('deviceTable')?.textContent).toBe('No devices registered')
  })
})
