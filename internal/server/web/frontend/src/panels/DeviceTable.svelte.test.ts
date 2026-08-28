// The device table is the page's identity surface: SFDI and LFDI are
// rendered per row and the assignment POST is keyed off the href's
// trailing segment, so both the rendered values and the request body are
// asserted against what was supplied.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import DeviceTable from './DeviceTable.svelte'
import * as api from '../lib/api'
import type { DashboardDevice } from '../lib/dashboard'
import type { AdminFSA } from '../lib/fsa'

const devices: DashboardDevice[] = [
  {
    sfdi: '123456789012',
    lfdi: '0123456789ABCDEF0123456789ABCDEF01234567',
    href: '/edev/3',
    enabled: true,
  },
  {
    sfdi: '210987654321',
    lfdi: 'FEDCBA9876543210FEDCBA9876543210FEDCBA98',
    href: '/edev/4',
    enabled: false,
  },
]

const fsas: AdminFSA[] = [
  { href: '/api/fsas/fsa-a', mRID: 'fsa-a', description: 'A', primacy: 1 },
  { href: '/api/fsas/fsa-b', mRID: 'fsa-b', description: 'B', primacy: 2 },
]

describe('DeviceTable', () => {
  it('renders each supplied SFDI, the truncated LFDI, and the enabled state', () => {
    render(DeviceTable, { props: { devices, fsas, onChanged: () => {} } })

    const sfdiCells = screen.getAllByTestId('device-sfdi')
    expect(sfdiCells[0]).toHaveTextContent('123456789012')
    expect(sfdiCells[1]).toHaveTextContent('210987654321')

    const lfdiCells = screen.getAllByTestId('device-lfdi')
    expect(lfdiCells[0]).toHaveTextContent('0123456789ABCDEF...')
    expect(lfdiCells[1]).toHaveTextContent('FEDCBA9876543210...')

    expect(screen.getByText('ONLINE')).toBeInTheDocument()
    expect(screen.getByText('OFFLINE')).toBeInTheDocument()
  })

  it('offers every FSA mRID as an assignment target on a per-device select', () => {
    const { container } = render(DeviceTable, { props: { devices, fsas, onChanged: () => {} } })

    const select = container.querySelector('#assignSel-3') as HTMLSelectElement | null
    expect(select).not.toBeNull()
    const values = Array.from(select!.options).map((o) => o.value)
    expect(values).toEqual(['', '/api/fsas/fsa-a', '/api/fsas/fsa-b'])
  })

  it('assigns the selected FSA to the device id taken from the href, not the row index', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: {} })
    const onChanged = vi.fn()
    const { container } = render(DeviceTable, { props: { devices, fsas, onChanged } })

    const select = container.querySelector('#assignSel-4') as HTMLSelectElement
    await fireEvent.change(select, { target: { value: '/api/fsas/fsa-b' } })
    const assignButtons = screen.getAllByRole('button', { name: 'Assign' })
    await fireEvent.click(assignButtons[1])

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/devices/4/fsa-assignment', { fsaHref: '/api/fsas/fsa-b' })
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it('renders an explicit empty state for a fleet with no devices', () => {
    render(DeviceTable, { props: { devices: [], fsas, onChanged: () => {} } })

    expect(screen.getByText('No devices registered')).toBeInTheDocument()
  })

  it('surfaces an assignment failure instead of silently doing nothing', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, error: 'device not found', status: 404 })
    const { container } = render(DeviceTable, { props: { devices, fsas, onChanged: () => {} } })

    const select = container.querySelector('#assignSel-3') as HTMLSelectElement
    await fireEvent.change(select, { target: { value: '/api/fsas/fsa-a' } })
    await fireEvent.click(screen.getAllByRole('button', { name: 'Assign' })[0])

    const err = await screen.findByTestId('device-table-error')
    expect(err).toHaveTextContent('Assign failed (404): device not found')
  })
})
