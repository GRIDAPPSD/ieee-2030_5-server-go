import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/svelte'
import Overview from './Overview.svelte'
import type { CommsState, DashboardDER, DashboardData, DashboardDevice } from '../lib/dashboard'

const device = (comms: CommsState) => ({
  sfdi: '1',
  lfdi: '1',
  href: '/edev/1',
  enabled: true,
  lastRequest: null,
  comms,
  lastKnown: false,
  ders: [],
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

describe('Overview DER stat', () => {
  const der = (over: Partial<DashboardDER> = {}): DashboardDER => ({
    id: '0',
    reported: true,
    connect: { source: 'genConnectStatus', raw: 1, since: 0, connected: true, available: false, operating: false, test: false, fault: false, reservedBits: 0 },
    inverter: null,
    readingTime: 1,
    ageSeconds: 5,
    stale: false,
    noReadingTime: false,
    clockAhead: false,
    ...over,
  })
  const dev = (ders: DashboardDER[], over: Partial<DashboardDevice> = {}): DashboardDevice => ({
    ...device('online'),
    lastKnown: false,
    ders,
    ...over,
  })
  const text = (d: DashboardData) => {
    const { container } = render(Overview, { props: { data: d } })
    return container.querySelector('#derConnected')?.textContent?.replace(/\s+/g, ' ').trim()
  }

  it('counts fresh connected DERs and separately the stale ones', () => {
    const connectedDown = der({ connect: { ...der().connect!, raw: 0, connected: false } })
    const d: DashboardData = {
      ...data,
      deviceCount: 3,
      devices: [dev([der()]), dev([der({ stale: true, ageSeconds: 4000 })]), dev([connectedDown])],
    }
    expect(text(d)).toBe('1, stale 1')
  })

  it('counts a hybrid connected through also only', () => {
    const c = { ...der().connect!, raw: 0, connected: false, also: { ...der().connect!, source: 'storConnectStatus' } }
    expect(text({ ...data, deviceCount: 1, devices: [dev([der({ connect: c })])] })).toBe('1, stale 0')
  })

  it('does not count a last-known DER of an offline device as reporting connected', () => {
    expect(text({ ...data, deviceCount: 1, devices: [dev([der()], { comms: 'offline', lastKnown: true })] })).toBe('0, stale 0')
  })

  it('says Not reported when a DER has no status', () => {
    expect(text({ ...data, deviceCount: 1, devices: [dev([der({ reported: false, connect: null })])] })).toBe('Not reported')
  })

  it('says Not reported when no device has a DER', () => {
    expect(text({ ...data, deviceCount: 1, devices: [dev([])] })).toBe('Not reported')
  })
})
