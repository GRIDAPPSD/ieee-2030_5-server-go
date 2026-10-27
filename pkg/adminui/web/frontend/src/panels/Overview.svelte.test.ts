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

describe('Overview DER counts', () => {
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
  const operating = (over: Partial<DashboardDER> = {}) => der({ connect: { ...der().connect!, operating: true }, ...over })
  const dev = (ders: DashboardDER[], over: Partial<DashboardDevice> = {}): DashboardDevice => ({
    ...device('online'),
    lastKnown: false,
    ders,
    ...over,
  })
  const counts = (d: DashboardData | null) => {
    const { container } = render(Overview, { props: { data: d } })
    const read = (id: string) => container.querySelector(id)?.textContent?.trim()
    return { registered: read('#bigDeviceCount'), connected: read('#derConnected'), operating: read('#derOperating') }
  }

  it('shows registered, connected and operating as separate plain numbers for a seeded fleet', () => {
    const disconnected = der({ connect: { ...der().connect!, raw: 0, connected: false } })
    const d: DashboardData = {
      ...data,
      deviceCount: 5,
      devices: [dev([operating()]), dev([der()]), dev([disconnected]), dev([operating({ stale: true })]), dev([operating()], { comms: 'offline', lastKnown: true })],
    }
    expect(counts(d)).toEqual({ registered: '5', connected: '2', operating: '1' })
  })

  it('does not count a DER with no readable status as connected or operating', () => {
    const d: DashboardData = {
      ...data,
      deviceCount: 3,
      devices: [dev([der({ reported: false, connect: null })]), dev([], { derError: 'boom' }), dev([])],
    }
    expect(counts(d)).toEqual({ registered: '3', connected: '0', operating: '0' })
  })

  it('counts a hybrid connected and operating through also only', () => {
    const c = { ...der().connect!, raw: 0, connected: false, also: { ...der().connect!, source: 'storConnectStatus', operating: true } }
    expect(counts({ ...data, deviceCount: 1, devices: [dev([der({ connect: c })])] })).toEqual({ registered: '1', connected: '1', operating: '1' })
  })

  it('counts every DER once, not every device', () => {
    const d: DashboardData = { ...data, deviceCount: 1, devices: [dev([operating(), operating({ id: '1' })])] }
    expect(counts(d)).toEqual({ registered: '1', connected: '2', operating: '2' })
  })

  it('notes unreadable devices only when there are some, without changing the counts', () => {
    const none = render(Overview, { props: { data: { ...data, deviceCount: 1, devices: [dev([der()])] } } })
    expect(none.container.querySelector('#derUnreadable')).toBeNull()
    none.unmount()

    const d: DashboardData = { ...data, deviceCount: 3, devices: [dev([der()]), dev([], { derError: 'boom' }), dev([], { derError: 'bang' })] }
    const { container } = render(Overview, { props: { data: d } })
    expect(container.querySelector('#derUnreadable')).toHaveTextContent('2')
    expect(container.querySelector('#derConnected')).toHaveTextContent('1')
  })

  it('counts devices under a control and notes unknown ones apart from the count', () => {
    const d: DashboardData = {
      ...data,
      deviceCount: 5,
      devices: [
        dev([], { underControl: true }),
        dev([], { underControl: true }),
        dev([], { underControl: false }),
        dev([], { underControl: null, controlError: 'store closed' }),
        dev([], { underControl: null, controlError: 'store closed' }),
      ],
    }
    const { container } = render(Overview, { props: { data: d } })
    expect(container.querySelector('#derUnderControl')?.textContent?.trim()).toBe('2')
    expect(container.querySelector('#derControlUnknown')?.textContent?.trim()).toBe('2 unknown')
  })

  it('shows no unknown row when every device control state is known', () => {
    const d: DashboardData = { ...data, deviceCount: 2, devices: [dev([], { underControl: true }), dev([], { underControl: false })] }
    const { container } = render(Overview, { props: { data: d } })
    expect(container.querySelector('#derUnderControl')?.textContent?.trim()).toBe('1')
    expect(container.querySelector('#derControlUnknown')).toBeNull()
  })

  it('shows zeroes before the first update', () => {
    expect(counts(null)).toEqual({ registered: '0', connected: '0', operating: '0' })
  })
})
