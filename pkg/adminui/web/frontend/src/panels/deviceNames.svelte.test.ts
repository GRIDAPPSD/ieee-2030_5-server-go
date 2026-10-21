// Every view that shows a device labels it through deviceLabel (#1034): the
// entered name, and the SFDI only when there is none. Each view is rendered
// with one named and one unnamed device, so a view that kept reading the SFDI
// directly fails on the named one.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import * as api from '../lib/api'
import { resetDeviceProfiles } from '../lib/devices'
import { seedDeviceProfiles } from '../lib/devices.fixture'
import type { DashboardDevice } from '../lib/dashboard'
import type { TopologyNode } from '../lib/fsa'
import DeviceTable from './DeviceTable.svelte'
import DerControl from './DerControl.svelte'
import LookupDevice from './LookupDevice.svelte'
import TopologyTree from './TopologyTree.svelte'
import ManagementPairs from './ManagementPairs.svelte'
import FleetPane from './FleetPane.svelte'
import DispatchPane from './DispatchPane.svelte'

const NAMED = { lfdi: '0123456789ABCDEF0123456789ABCDEF01234567', sfdi: '167261211635', name: 'Inverter 3' }
const PLAIN = { lfdi: 'FEDCBA9876543210FEDCBA9876543210FEDCBA98', sfdi: '222222222222' }

function device(d: { lfdi: string; sfdi: string }, n: number): DashboardDevice {
  return { sfdi: d.sfdi, lfdi: d.lfdi, href: `/edev/${n}`, enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: false, ders: [] }
}
const devices = [device(NAMED, 1), device(PLAIN, 2)]

async function seed(name = NAMED.name) {
  await seedDeviceProfiles([{ lfdi: NAMED.lfdi, name, kind: 'device' }])
}

// Anything a view reads beyond the data under test answers with a failure the
// view already handles, so no test depends on an unrelated route.
function failOthers(routes: Record<string, unknown> = {}) {
  return vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    for (const [prefix, data] of Object.entries(routes)) {
      if (path.startsWith(prefix)) return { ok: true, data } as never
    }
    return { ok: false, error: 'not mocked', status: 500 }
  })
}

afterEach(() => {
  vi.restoreAllMocks()
  resetDeviceProfiles()
})

describe('Devices tab', () => {
  it('labels a named device by name and an unnamed one by SFDI', async () => {
    await seed()
    failOthers()
    render(DeviceTable, { props: { devices, fsas: [], onChanged: () => {} } })

    const labels = screen.getAllByTestId('device-label').map((e) => e.textContent)
    expect(labels).toEqual(['Inverter 3', '222222222222'])
  })

  it('renders a name containing markup as text', async () => {
    await seed('<script>alert(1)</script>')
    failOthers()
    const { container } = render(DeviceTable, { props: { devices, fsas: [], onChanged: () => {} } })

    expect(screen.getAllByTestId('device-label')[0].textContent).toBe('<script>alert(1)</script>')
    expect(container.querySelector('script')).toBeNull()
  })

  it('renames a device: puts the name with the held kind and shows the new label', async () => {
    await seed()
    let stored = { lfdi: NAMED.lfdi, name: NAMED.name, kind: 'device' }
    const put = vi.spyOn(api, 'putJSON').mockImplementation(async (_path: string, body: unknown) => {
      stored = { ...stored, name: (body as { name: string }).name }
      return { ok: true, data: stored } as never
    })
    vi.spyOn(api, 'fetchJSON').mockImplementation(async () => ({ ok: true, data: { profiles: [stored] } }) as never)
    render(DeviceTable, { props: { devices, fsas: [], onChanged: () => {} } })

    await fireEvent.click(screen.getByRole('button', { name: 'Edit name of device Inverter 3' }))
    const input = screen.getByTestId('device-name-input') as HTMLInputElement
    expect(input.value).toBe('Inverter 3')
    await fireEvent.input(input, { target: { value: '  Roof array  ' } })
    await fireEvent.click(screen.getByTestId('device-name-save'))

    await waitFor(() => expect(screen.getAllByTestId('device-label')[0].textContent).toBe('Roof array'))
    expect(put).toHaveBeenCalledWith(`/api/device-profiles/${NAMED.lfdi}`, { name: 'Roof array', kind: 'device' })
    expect(screen.queryByTestId('device-name-input')).toBeNull()
  })

  it('shows the server refusal and keeps the editor open when a name is taken', async () => {
    await seed()
    vi.spyOn(api, 'putJSON').mockResolvedValue({ ok: false, error: 'name already in use by another device', status: 409 })
    failOthers()
    render(DeviceTable, { props: { devices, fsas: [], onChanged: () => {} } })

    await fireEvent.click(screen.getByRole('button', { name: 'Edit name of device 222222222222' }))
    await fireEvent.input(screen.getByTestId('device-name-input'), { target: { value: 'Inverter 3' } })
    await fireEvent.click(screen.getByTestId('device-name-save'))

    expect(await screen.findByTestId('device-rename-error')).toHaveTextContent(
      'Rename failed (409): name already in use by another device',
    )
    expect(screen.getByTestId('device-name-input')).toBeInTheDocument()
  })
})

describe('Control window device target', () => {
  it('offers the name for a named device and the SFDI for an unnamed one', async () => {
    await seed()
    failOthers()
    render(DerControl, { props: { devices } })

    const select = screen.getByRole('combobox', { name: 'Device' })
    expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual([
      '(pick device)',
      'Inverter 3',
      '222222222222',
    ])
  })
})

describe('Lookup device', () => {
  async function lookup(d: { lfdi: string; sfdi: string }, n: number) {
    failOthers({ '/api/devices/by-lfdi/': { found: true, device: { sfdi: d.sfdi, lfdi: d.lfdi, href: `/edev/${n}`, enabled: true } } })
    const { container } = render(LookupDevice)
    await fireEvent.input(container.querySelector('#lookupLFDI') as HTMLInputElement, { target: { value: d.lfdi } })
    await fireEvent.click(screen.getByRole('button', { name: 'Lookup' }))
    return container.querySelector('#lookupResult') as HTMLElement
  }

  it('shows the name of a named device', async () => {
    await seed()
    const result = await lookup(NAMED, 1)
    await waitFor(() => expect(result).toHaveTextContent('Found: /edev/1 Inverter 3 enabled=true'))
  })

  it('shows the SFDI of an unnamed device', async () => {
    await seed()
    const result = await lookup(PLAIN, 2)
    await waitFor(() => expect(result).toHaveTextContent('Found: /edev/2 222222222222 enabled=true'))
  })
})

describe('Topology', () => {
  function tree(): TopologyNode {
    return {
      kind: 'SY',
      id: 'sy',
      label: 'System',
      children: [
        { kind: 'DEV', id: '1', label: 'edev 1', sfdi: NAMED.sfdi, lfdi: NAMED.lfdi, enabled: true },
        { kind: 'DEV', id: '2', label: 'edev 2', sfdi: PLAIN.sfdi, lfdi: PLAIN.lfdi, enabled: false },
      ],
    }
  }

  it('shows the name for a named device node and the SFDI for an unnamed one', async () => {
    await seed()
    failOthers()
    render(TopologyTree, { props: { tree: tree(), error: '', onRefresh: () => {} } })

    expect(screen.getByTestId('topology-node-DEV-1')).toHaveTextContent('DEV edev 1 (Inverter 3, ON)')
    expect(screen.getByTestId('topology-node-DEV-2')).toHaveTextContent('DEV edev 2 (222222222222, OFF)')
  })
})

describe('Management pairs pickers', () => {
  it('offer the name for a named device and the SFDI for an unnamed one, in both pickers', async () => {
    await seed()
    failOthers({ '/api/management-pairs': { managerLFDI: NAMED.lfdi, managedLFDIs: [] } })
    render(ManagementPairs, { props: { devices, ready: true } })

    for (const label of ['Manager', 'Managed device']) {
      const options = within(screen.getByLabelText(label)).getAllByRole('option').map((o) => o.textContent)
      expect(options.slice(1)).toEqual(['Inverter 3 (01234567...)', '222222222222 (FEDCBA98...)'])
    }
  })
})

describe('Management pairs table', () => {
  it('labels both ends of a pair by name or SFDI', async () => {
    await seed()
    vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      const manager = new URL(path, 'http://x').searchParams.get('manager')
      const managed = manager === NAMED.lfdi ? [PLAIN.lfdi] : []
      return { ok: true, data: { managerLFDI: manager, managedLFDIs: managed } } as never
    })
    render(ManagementPairs, { props: { devices, ready: true } })

    const row = await screen.findByTestId('pair-row')
    expect(within(row).getByTestId('pair-manager-label').textContent).toBe('Inverter 3')
    expect(within(row).getByTestId('pair-managed-label').textContent).toBe('222222222222')
  })
})

const zero = { sum: 0, unreported: 1, stale: 0 }
const rollup = { deviceCount: 1, connected: 0, alarmed: 0, stale: 0, p: zero, q: zero, statWAvail: zero, statVarAvail: zero }
const fleets = [
  { aggregatorLFDI: NAMED.lfdi, devices: [{ lfdi: PLAIN.lfdi, edevId: '2', href: '/edev/2', measurements: {} }], rollup },
  { aggregatorLFDI: PLAIN.lfdi, devices: [{ lfdi: NAMED.lfdi, edevId: '1', href: '/edev/1', measurements: {} }], rollup },
]

describe('DERMS fleets', () => {
  it('names a named aggregator and shows the LFDI prefix of an unnamed one', async () => {
    await seed()
    failOthers({ '/api/derms/fleets': fleets })
    render(FleetPane)

    const rows = await screen.findAllByTestId('fleet-row')
    expect(within(rows[0]).getAllByRole('cell')[0].textContent).toBe('Inverter 3')
    expect(within(rows[1]).getAllByRole('cell')[0].textContent).toBe('FEDCBA9876543210...')
  })
})

describe('DERMS dispatch', () => {
  it('names fleets and devices in the pickers and in the confirm text', async () => {
    await seed()
    failOthers({ '/api/derms/fleets': fleets })
    render(DispatchPane)

    const fleetSelect = await screen.findByTestId('dispatch-fleet')
    expect(within(fleetSelect).getAllByRole('option').map((o) => o.textContent)).toEqual([
      '(pick fleet)',
      'Inverter 3 (1 devices)',
      'FEDCBA9876543210... (1 devices)',
    ])

    await fireEvent.change(fleetSelect, { target: { value: PLAIN.lfdi } })
    await fireEvent.click(screen.getByRole('radio', { name: 'Plain dispatch' }))
    const deviceSelect = await screen.findByTestId('dispatch-device')
    expect(within(deviceSelect).getAllByRole('option').map((o) => o.textContent)).toEqual([
      '(pick device)',
      'Inverter 3',
    ])

    await fireEvent.change(fleetSelect, { target: { value: NAMED.lfdi } })
    const plainSelect = await screen.findByTestId('dispatch-device')
    expect(within(plainSelect).getAllByRole('option').map((o) => o.textContent)).toEqual([
      '(pick device)',
      'EndDevice 2 (FEDCBA9876543210...)',
    ])
  })
})
