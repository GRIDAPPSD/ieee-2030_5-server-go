// Asserts the header renders the SUPPLIED values, not merely that the
// badge elements exist: an operator reads TLS mode, uptime and the device
// count off this bar and nothing else on the page repeats them.
import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/svelte'
import NavBar from './NavBar.svelte'
import type { DashboardData } from '../lib/dashboard'

const data: DashboardData = {
  timestamp: '12:34:56',
  deviceCount: 7,
  mupCount: 3,
  tlsMode: 'TLS_AES_128_GCM_SHA256',
  uptime: '1h2m3s',
  devices: [],
}

describe('NavBar', () => {
  it('renders the supplied TLS mode, uptime and device count', () => {
    const { container } = render(NavBar, { props: { data } })

    expect(container.querySelector('#tlsMode')).toHaveTextContent('TLS_AES_128_GCM_SHA256')
    expect(container.querySelector('#uptime')).toHaveTextContent('1h2m3s')
    expect(container.querySelector('#deviceCount')).toHaveTextContent('7')
    expect(container.querySelector('.navbar h1')).toHaveTextContent('IEEE 2030.5 Server Admin')
  })

  it('renders placeholders, not stale or zeroed identity, before the first update', () => {
    const { container } = render(NavBar, { props: { data: null } })

    expect(container.querySelector('#tlsMode')).toHaveTextContent('-')
    expect(container.querySelector('#uptime')).toHaveTextContent('-')
    expect(container.querySelector('#deviceCount')).toHaveTextContent('0')
  })
})
