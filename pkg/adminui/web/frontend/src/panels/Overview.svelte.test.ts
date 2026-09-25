import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/svelte'
import Overview from './Overview.svelte'
import type { DashboardData } from '../lib/dashboard'

const data: DashboardData = {
  timestamp: '12:34:56',
  deviceCount: 12,
  mupCount: 45,
  tlsMode: 'GCM',
  uptime: '5m',
  devices: [],
}

describe('Overview', () => {
  it('renders the supplied device and MUP counts', () => {
    const { container } = render(Overview, { props: { data } })

    expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('12')
    expect(container.querySelector('#mupCount')).toHaveTextContent('45')
  })

  it('renders zeroes before the first update', () => {
    const { container } = render(Overview, { props: { data: null } })

    expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('0')
    expect(container.querySelector('#mupCount')).toHaveTextContent('0')
  })
})
