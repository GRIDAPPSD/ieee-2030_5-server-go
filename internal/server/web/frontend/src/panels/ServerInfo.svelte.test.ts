import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/svelte'
import ServerInfo from './ServerInfo.svelte'
import type { DashboardData } from '../lib/dashboard'

describe('ServerInfo', () => {
  it('renders the supplied TLS mode and uptime alongside the fixed protocol line', () => {
    const data: DashboardData = {
      timestamp: '00:00:01',
      deviceCount: 0,
      mupCount: 0,
      tlsMode: 'TLS_CHACHA20_POLY1305_SHA256',
      uptime: '42s',
      devices: null,
    }

    const { container } = render(ServerInfo, { props: { data } })

    expect(container.querySelector('#tlsCipher')).toHaveTextContent('TLS_CHACHA20_POLY1305_SHA256')
    expect(container.querySelector('#uptimeDetail')).toHaveTextContent('42s')
    expect(container.textContent).toContain('IEEE 2030.5-2018')
  })
})
