// The device-reported columns: what a DER says about its own connection and
// inverter, and how fresh that report is. Cells are asserted by the labels,
// chips and hover text the operator sees, never by a render alone.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import DeviceTable from './DeviceTable.svelte'
import type { DashboardDER, DashboardDevice, DerConnect } from '../lib/dashboard'

const connect = (raw: number, over: Partial<DerConnect> = {}): DerConnect => ({
  source: 'genConnectStatus',
  raw,
  since: 1759665600,
  connected: (raw & 0x01) !== 0,
  available: (raw & 0x02) !== 0,
  operating: (raw & 0x04) !== 0,
  test: (raw & 0x08) !== 0,
  fault: (raw & 0x10) !== 0,
  reservedBits: raw & 0xe0,
  ...over,
})

const der = (over: Partial<DashboardDER> = {}): DashboardDER => ({
  id: '0',
  reported: true,
  connect: connect(0x01),
  inverter: { code: 4, since: 1759665600 },
  readingTime: 1759665590,
  ageSeconds: 10,
  stale: false,
  noReadingTime: false,
  clockAhead: false,
  ...over,
})

const device = (over: Partial<DashboardDevice> = {}): DashboardDevice => ({
  sfdi: '123456789012',
  lfdi: '0123456789ABCDEF0123456789ABCDEF01234567',
  href: '/edev/3',
  enabled: true,
  lastRequest: '2026-10-05T11:59:30Z',
  comms: 'online',
  lastKnown: false,
  ders: [der()],
  ...over,
})

const renderOne = (d: DashboardDevice) => render(DeviceTable, { props: { devices: [d], fsas: [], onChanged: () => {} } })
const connCell = () => screen.getByTestId('device-der-connection')
const invCell = () => screen.getByTestId('device-inverter')
const chips = (cell: HTMLElement) => Array.from(cell.querySelectorAll('.chip')).map((c) => c.textContent?.trim())

describe('DeviceTable device-reported columns', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-05T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('has the two reported column headers', () => {
    renderOne(device())
    expect(screen.getByRole('columnheader', { name: 'DER connection (reported)' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Inverter state (reported)' })).toBeInTheDocument()
  })

  it('renders value 0 as Disconnected with the raw 0x00 on hover, not as unknown', () => {
    renderOne(device({ ders: [der({ connect: connect(0x00) })] }))
    expect(chips(connCell())).toEqual(['Disconnected'])
    expect(connCell().getAttribute('title')).toContain('0x00')
    expect(connCell()).not.toHaveTextContent('Not reported')
  })

  it('renders 0x0B as Connected, Available and Test, with Test styled as a warning and no Fault', () => {
    renderOne(device({ ders: [der({ connect: connect(0x0b) })] }))
    expect(chips(connCell())).toEqual(['Connected', 'Available', 'Test'])
    expect(connCell().querySelector('.chip.warn')?.textContent?.trim()).toBe('Test')
    expect(connCell().querySelectorAll('.chip.warn')).toHaveLength(1)
    expect(connCell().getAttribute('title')).toContain('0x0B')
    expect(connCell().getAttribute('title')).toContain('genConnectStatus')
  })

  it('shows Fault with warning styling', () => {
    renderOne(device({ ders: [der({ connect: connect(0x11) })] }))
    expect(chips(connCell())).toEqual(['Connected', 'Fault'])
    expect(connCell().querySelector('.chip.warn')?.textContent?.trim()).toBe('Fault')
  })

  it('never drops set reserved bits', () => {
    renderOne(device({ ders: [der({ connect: connect(0x81) })] }))
    expect(chips(connCell())).toEqual(['Connected', 'Reserved bits 0x80'])
  })

  it('says on hover that Connected does not mean energized or exporting', () => {
    renderOne(device())
    expect(connCell().getAttribute('title')).toContain('Does not mean energized or exporting')
  })

  it('shows Energized for a 2023 connectStatus and names that source', () => {
    const c = connect(0x03, { source: 'connectStatus', energized: true, available: false, reservedBits: 0 })
    renderOne(device({ ders: [der({ connect: c })] }))
    expect(chips(connCell())).toEqual(['Connected', 'Energized'])
    expect(connCell().getAttribute('title')).toContain('connectStatus')
  })

  it('renders a hybrid whose only the storage field (also) is connected as Connected', () => {
    const c = connect(0x00, { also: connect(0x07, { source: 'storConnectStatus' }) })
    renderOne(device({ ders: [der({ connect: c })] }))
    expect(chips(connCell())[0]).toBe('Connected')
    expect(chips(connCell())).not.toContain('Disconnected')
    expect(connCell().getAttribute('title')).toContain('storConnectStatus')
    expect(connCell().getAttribute('title')).toContain('0x07')
    expect(connCell().getAttribute('title')).toContain('0x00')
  })

  it('shows Not reported for a DER with no connect status and for a DER with no status at all', () => {
    renderOne(device({ ders: [der({ connect: null, inverter: null }), der({ id: '1', reported: false, connect: null, inverter: null })] }))
    expect(connCell()).toHaveTextContent('Not reported')
    expect(chips(connCell())).toEqual([])
    expect(connCell()).not.toHaveTextContent('mixed')
  })

  it('labels inverter code 4 Running and 0 N/A, 11 and above Reserved with the number', () => {
    const { unmount } = renderOne(device({ ders: [der({ inverter: { code: 4, since: 0 } })] }))
    expect(invCell()).toHaveTextContent('Running')
    expect(invCell()).not.toHaveTextContent('MPPT')
    unmount()
    const second = renderOne(device({ ders: [der({ inverter: { code: 0, since: 0 } })] }))
    expect(invCell()).toHaveTextContent('N/A')
    second.unmount()
    renderOne(device({ ders: [der({ inverter: { code: 12, since: 0 } })] }))
    expect(invCell()).toHaveTextContent('Reserved (12)')
  })

  it('gives code 8 a safety tooltip that output may be energized in service', () => {
    renderOne(device({ ders: [der({ inverter: { code: 8, since: 0 } })] }))
    expect(invCell()).toHaveTextContent('Standby (service)')
    expect(invCell().getAttribute('title')).toContain('Output may be energized while in service')
    expect(invCell().getAttribute('title')).toContain('8')
  })

  it('shows Not reported when the DER sent no inverterStatus', () => {
    renderOne(device({ ders: [der({ inverter: null })] }))
    expect(invCell()).toHaveTextContent('Not reported')
  })

  it('shows Stale with its age on both reported columns, still showing what was reported', () => {
    renderOne(device({ ders: [der({ stale: true, ageSeconds: 2400 })] }))
    expect(connCell()).toHaveTextContent('Stale, 40m old')
    expect(invCell()).toHaveTextContent('Stale, 40m old')
    expect(chips(connCell())).toEqual(['Connected'])
  })

  it('shows No reading time instead of an age', () => {
    renderOne(device({ ders: [der({ stale: true, noReadingTime: true, readingTime: 0, ageSeconds: 0 })] }))
    expect(connCell()).toHaveTextContent('No reading time')
    expect(connCell()).not.toHaveTextContent('Stale')
    expect(connCell()).not.toHaveTextContent('0s')
  })

  it('shows Clock ahead rather than a fresh age', () => {
    renderOne(device({ ders: [der({ stale: true, clockAhead: true, readingTime: 4102444800, ageSeconds: 0 })] }))
    expect(connCell()).toHaveTextContent('Clock ahead')
    expect(connCell()).not.toHaveTextContent('Stale')
  })

  it('shows Last known with the age for an offline device, keeping the last reported chips', () => {
    renderOne(device({ comms: 'offline', lastKnown: true, ders: [der({ connect: connect(0x07), ageSeconds: 300 })] }))
    expect(connCell()).toHaveTextContent('Last known, 5m old')
    expect(invCell()).toHaveTextContent('Last known, 5m old')
    expect(chips(connCell())).toEqual(['Connected', 'Available', 'Operating'])
  })

  it('shows no freshness note for a fresh reading from a device that is talking', () => {
    renderOne(device())
    expect(connCell()).not.toHaveTextContent('Stale')
    expect(connCell()).not.toHaveTextContent('Last known')
    expect(connCell().querySelectorAll('.note')).toHaveLength(0)
  })

  it('shows mixed with per-DER values on hover when DERs disagree', () => {
    renderOne(device({
      ders: [der({ id: '0', connect: connect(0x01) }), der({ id: '1', connect: connect(0x00), inverter: { code: 7, since: 0 } })],
    }))
    expect(chips(connCell())).toEqual(['mixed'])
    const title = connCell().getAttribute('title') ?? ''
    expect(title).toContain('DER 0: Connected')
    expect(title).toContain('DER 1: Disconnected')
    expect(chips(invCell())).toEqual(['mixed'])
    expect(invCell().getAttribute('title')).toContain('DER 0: Running')
    expect(invCell().getAttribute('title')).toContain('DER 1: Fault')
  })

  it('shows one value, not mixed, when several DERs agree', () => {
    renderOne(device({ ders: [der({ id: '0' }), der({ id: '1' })] }))
    expect(chips(connCell())).toEqual(['Connected'])
  })

  it('renders a derError on the row, and not as No DERs, even with no DERs listed', () => {
    renderOne(device({ ders: [], derError: 'DERs.List("3"): store closed' }))
    expect(screen.getByTestId('device-der-error')).toHaveTextContent('DER status unreadable: DERs.List("3"): store closed')
    expect(connCell()).not.toHaveTextContent('No DERs')
    expect(invCell()).toHaveTextContent('Unreadable')
  })

  it('keeps the DERs that did read beside a derError', () => {
    renderOne(device({ ders: [der()], derError: 'DER 1 status: boom' }))
    expect(chips(connCell())).toEqual(['Connected'])
    expect(screen.getByTestId('device-der-error')).toHaveTextContent('DER 1 status: boom')
  })

  it('says No DERs for a device with none and no error', () => {
    renderOne(device({ ders: [] }))
    expect(connCell()).toHaveTextContent('No DERs')
    expect(screen.queryByTestId('device-der-error')).not.toBeInTheDocument()
  })

  it('keeps a Fault warning on the mixed chip and each DER hover line, with the tooltips', () => {
    renderOne(device({
      ders: [der({ id: '0', connect: connect(0x01) }), der({ id: '1', connect: connect(0x11), inverter: { code: 8, since: 0 } })],
    }))
    expect(chips(connCell())).toEqual(['mixed'])
    expect(connCell().querySelector('.chip.warn')?.textContent?.trim()).toBe('mixed')
    const title = connCell().getAttribute('title') ?? ''
    expect(title).toContain('DER 1: Connected - Fault')
    expect(title).toContain('Does not mean energized or exporting')
    expect(invCell().querySelector('.chip.warn')?.textContent?.trim()).toBe('mixed')
    expect(invCell().getAttribute('title')).toContain('Output may be energized while in service')
  })

  it('leaves the mixed chip unstyled when no DER carries a warning', () => {
    renderOne(device({ ders: [der({ id: '0', connect: connect(0x01) }), der({ id: '1', connect: connect(0x00) })] }))
    expect(connCell().querySelectorAll('.chip.warn')).toHaveLength(0)
  })

  it('styles inverter code 7 and code 8 as warnings and code 4 as not', () => {
    const warn = (code: number) => {
      const { unmount } = renderOne(device({ ders: [der({ inverter: { code, since: 0 } })] }))
      const w = invCell().querySelectorAll('.chip.warn').length
      unmount()
      return w
    }
    expect([warn(4), warn(7), warn(8)]).toEqual([0, 1, 1])
  })

  it('shows Not energized for a 2023 report with the Energized bit clear, and Energized when set', () => {
    const c = (raw: number) => connect(raw, { source: 'connectStatus', energized: (raw & 0x02) !== 0, available: false, reservedBits: 0 })
    const { unmount } = renderOne(device({ ders: [der({ connect: c(0x01) })] }))
    expect(chips(connCell())).toEqual(['Connected', 'Not energized'])
    unmount()
    renderOne(device({ ders: [der({ connect: c(0x03) })] }))
    expect(chips(connCell())).toEqual(['Connected', 'Energized'])
  })

  it('does not invent an energized state for a 2018 report', () => {
    renderOne(device({ ders: [der({ connect: connect(0x01) })] }))
    expect(connCell()).not.toHaveTextContent('energized')
  })

  it('names the stale DER and its age when one DER is stale beside a fresh one', () => {
    renderOne(device({
      ders: [der({ id: '0', ageSeconds: 10 }), der({ id: '1', stale: true, ageSeconds: 2400 })],
    }))
    expect(connCell()).toHaveTextContent('1 of 2 stale, oldest 40m old')
    expect(connCell()).not.toHaveTextContent('Stale, 10s old')
  })

  it('gives the stale age, not the fresh one, when every DER is stale', () => {
    renderOne(device({ ders: [der({ id: '0', stale: true, ageSeconds: 2000 }), der({ id: '1', stale: true, ageSeconds: 3600 })] }))
    expect(connCell()).toHaveTextContent('Stale, 1h old')
  })
})
