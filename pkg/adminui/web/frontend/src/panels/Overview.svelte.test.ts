import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/svelte'
import Overview from './Overview.svelte'
import type { CommsState, DashboardData } from '../lib/dashboard'

const device = (comms: CommsState) => ({
  sfdi: '1',
  lfdi: '1',
  href: '/edev/1',
  enabled: true,
  lastRequest: null,
  comms,
})

const data: DashboardData = {
  timestamp: '12:34:56',
  deviceCount: 12,
  mupCount: 45,
  tlsMode: 'GCM',
  uptime: '5m',
  commsOfflineAfterSeconds: 300,
  devices: [],
}

describe('Overview', () => {
  it('renders the supplied device and MUP counts', () => {
    const { container } = render(Overview, { props: { data } })

    expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('12')
    expect(container.querySelector('#mupCount')).toHaveTextContent('45')
  })

  it('counts only online devices against the registered total', () => {
    const withComms: DashboardData = {
      ...data,
      deviceCount: 4,
      devices: [device('online'), device('online'), device('offline'), device('not_seen')],
    }
    const { container } = render(Overview, { props: { data: withComms } })

    expect(container.querySelector('#commsOnline')).toHaveTextContent('2 of 4')
    expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('4')
  })

  it('reads 0 of N when no device has been seen', () => {
    const fresh: DashboardData = { ...data, deviceCount: 2, devices: [device('not_seen'), device('not_seen')] }
    const { container } = render(Overview, { props: { data: fresh } })

    expect(container.querySelector('#commsOnline')).toHaveTextContent('0 of 2')
  })

  it('says Unknown, not 0 of N, when no recorder is wired', () => {
    const unwired: DashboardData = { ...data, deviceCount: 2, devices: [device('unknown'), device('unknown')] }
    const { container } = render(Overview, { props: { data: unwired } })

    expect(container.querySelector('#commsOnline')).toHaveTextContent('Unknown')
  })

  it('renders zeroes before the first update', () => {
    const { container } = render(Overview, { props: { data: null } })

    expect(container.querySelector('#bigDeviceCount')).toHaveTextContent('0')
    expect(container.querySelector('#mupCount')).toHaveTextContent('0')
    expect(container.querySelector('#commsOnline')).toHaveTextContent('0 of 0')
  })
})
