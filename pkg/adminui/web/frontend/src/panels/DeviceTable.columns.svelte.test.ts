// Embedder-supplied Devices columns: one header per column in order, cell
// text escaped, a "-" for a missing cell, and the failure visible on the header.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import DeviceTable from './DeviceTable.svelte'
import type { DashboardColumn, DashboardDevice } from '../lib/dashboard'

const device = (href: string, cells?: Record<string, string>): DashboardDevice => ({
  sfdi: '123456789012',
  lfdi: '0123456789ABCDEF0123456789ABCDEF01234567',
  href,
  enabled: true,
  lastRequest: null,
  comms: 'online',
  lastKnown: false,
  ders: [],
  cells,
})

const renderTable = (devices: DashboardDevice[], columns?: DashboardColumn[]) =>
  render(DeviceTable, { props: { devices, columns, fsas: [], onChanged: () => {} } })

const headers = () => screen.getAllByRole('columnheader').map((h) => h.textContent?.trim())

describe('DeviceTable embedder columns', () => {
  it('renders the same headers and colspan as before when there are no columns', () => {
    const { container } = renderTable([device('/edev/1')], [])
    expect(headers()).toEqual([
      'SFDI', 'LFDI', 'Enabled', 'Comms', 'Last request',
      'DER connection (reported)', 'Inverter state (reported)', 'Href', 'Assign FSA',
    ])
    expect(container.querySelectorAll('tbody tr:first-child td')).toHaveLength(9)
  })

  it('treats an undefined columns prop like an empty list', () => {
    renderTable([device('/edev/1')])
    expect(headers()).toHaveLength(9)
  })

  it('adds one header per column in order, and the cell text in that column', () => {
    const { container } = renderTable(
      [device('/edev/1', { name: 'Pump A', site: 'North' }), device('/edev/2', { name: 'Pump B', site: 'South' })],
      [{ id: 'name', label: 'Name' }, { id: 'site', label: 'Site' }],
    )
    const hs = headers()
    expect(hs).toContain('Name')
    expect(hs.indexOf('Name')).toBeLessThan(hs.indexOf('Site'))
    expect(hs).toHaveLength(11)
    const nameCells = screen.getAllByTestId('device-column-name').map((c) => c.textContent?.trim())
    const siteCells = screen.getAllByTestId('device-column-site').map((c) => c.textContent?.trim())
    expect(nameCells).toEqual(['Pump A', 'Pump B'])
    expect(siteCells).toEqual(['North', 'South'])
    const row = container.querySelector('tbody tr:first-child')!
    const tds = Array.from(row.querySelectorAll('td'))
    expect(tds.indexOf(screen.getAllByTestId('device-column-name')[0] as HTMLTableCellElement)).toBeLessThan(
      tds.indexOf(screen.getAllByTestId('device-column-site')[0] as HTMLTableCellElement),
    )
  })

  it('keeps the empty-state row spanning every column', () => {
    const { container } = renderTable([], [{ id: 'name', label: 'Name' }])
    expect(container.querySelector('tbody td')?.getAttribute('colspan')).toBe('10')
  })

  it('shows - for a device with no entry for the column', () => {
    renderTable([device('/edev/1', { name: 'Pump A' }), device('/edev/2'), device('/edev/3', {})], [{ id: 'name', label: 'Name' }])
    expect(screen.getAllByTestId('device-column-name').map((c) => c.textContent?.trim())).toEqual(['Pump A', '-', '-'])
  })

  it('reads only own cells, so an id like constructor does not show an inherited member', () => {
    renderTable([device('/edev/1', {})], [{ id: 'constructor', label: 'Ctor' }])
    expect(screen.getByTestId('device-column-constructor').textContent?.trim()).toBe('-')
  })

  it('renders a repeated column id once instead of failing the keyed list', () => {
    renderTable([device('/edev/1', { a: 'x' })], [{ id: 'a', label: 'First' }, { id: 'a', label: 'Second' }])
    expect(headers()).toContain('First')
    expect(headers()).not.toContain('Second')
    expect(screen.getAllByTestId('device-column-a')).toHaveLength(1)
  })

  it('escapes cell text instead of rendering markup', () => {
    const { container } = renderTable([device('/edev/1', { name: '<b>x</b>' })], [{ id: 'name', label: 'Name' }])
    expect(screen.getByTestId('device-column-name').textContent).toBe('<b>x</b>')
    expect(container.querySelector('tbody b')).toBeNull()
  })

  it('marks an errored column header with the error as hover text, and shows - in its cells', () => {
    renderTable(
      [device('/edev/1', { site: '-' })],
      [{ id: 'name', label: 'Name' }, { id: 'site', label: 'Site', error: 'source timed out' }],
    )
    const marker = screen.getByTestId('device-column-error-site')
    expect(marker.getAttribute('title')).toBe('source timed out')
    expect(marker.closest('th')).toHaveTextContent('Site')
    expect(screen.getByTestId('device-column-site')).toHaveTextContent('-')
    expect(screen.queryByTestId('device-column-error-name')).not.toBeInTheDocument()
  })
})
