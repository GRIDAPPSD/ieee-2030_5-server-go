// The row Edit sets a device's kind and, for an aggregator, its managed
// devices (#1035). Each test asserts the requests the server receives, in
// order, against a fake profile and pair store that keeps real state.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import * as api from '../lib/api'
import { resetDeviceProfiles } from '../lib/devices'
import { seedDeviceProfiles } from '../lib/devices.fixture'
import type { DashboardDevice } from '../lib/dashboard'
import DeviceTable from './DeviceTable.svelte'

const AGG = '0123456789ABCDEF0123456789ABCDEF01234567'
const DEV_A = 'FEDCBA9876543210FEDCBA9876543210FEDCBA98'
const DEV_B = '5555555555555555555555555555555555555555'

const dev = (sfdi: string, lfdi: string, n: number): DashboardDevice => ({
  sfdi, lfdi, href: `/edev/${n}`, enabled: true, lastRequest: null, comms: 'not_seen', lastKnown: false, ders: [],
})
const devices = [dev('111111111111', AGG, 1), dev('222222222222', DEV_A, 2), dev('333333333333', DEV_B, 3)]

let pairs: Map<string, string>
let calls: string[]
let refuseProfile: string | null
let serverKind = 'device'

beforeEach(async () => {
  pairs = new Map()
  calls = []
  refuseProfile = null
  serverKind = 'device'
  await seedDeviceProfiles([{ lfdi: AGG, name: 'Agg', kind: 'device' }])
  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    const url = new URL(path, 'http://x')
    if (url.pathname === '/api/management-pairs') {
      const manager = url.searchParams.get('manager')
      const managed = [...pairs].filter(([, m]) => m === manager).map(([d]) => d)
      return { ok: true, data: { managerLFDI: manager, managedLFDIs: managed } } as never
    }
    if (url.pathname === '/api/device-profiles') {
      return { ok: true, data: { profiles: [{ lfdi: AGG, name: 'Agg', kind: serverKind }] } } as never
    }
    return { ok: false, error: 'not mocked', status: 500 }
  })
  vi.spyOn(api, 'putJSON').mockImplementation(async (path: string, body: unknown) => {
    const b = body as { name: string; kind: string }
    calls.push(`put ${path} ${b.kind}`)
    if (refuseProfile) return { ok: false, error: refuseProfile, status: 409 }
    return { ok: true, data: { lfdi: AGG, ...b } } as never
  })
  vi.spyOn(api, 'postJSON').mockImplementation(async (_p: string, body: unknown) => {
    const b = body as { managerLFDI: string; managedLFDI: string }
    calls.push(`add ${b.managedLFDI}`)
    pairs.set(b.managedLFDI, b.managerLFDI)
    return { ok: true, data: b } as never
  })
  vi.spyOn(api, 'deleteJSON').mockImplementation(async (path: string) => {
    const managed = new URL(path, 'http://x').searchParams.get('managed') ?? ''
    calls.push(`remove ${managed}`)
    pairs.delete(managed)
    return { ok: true, data: undefined as never }
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  resetDeviceProfiles()
})

async function edit(label: string) {
  render(DeviceTable, { props: { devices, fsas: [], onChanged: () => {} } })
  await fireEvent.click(screen.getByRole('button', { name: `Edit device ${label}` }))
}

describe('Devices tab Edit kind and devices', () => {
  it('makes a device an aggregator: saves the kind first, then one pair per ticked device', async () => {
    await edit('Agg')
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'aggregator' } })
    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    await fireEvent.click(screen.getByRole('checkbox', { name: '222222222222' }))
    await fireEvent.click(screen.getByRole('checkbox', { name: '333333333333' }))
    await fireEvent.click(screen.getByTestId('device-name-save'))

    await waitFor(() => expect(screen.queryByTestId('device-name-input')).toBeNull())
    expect(calls[0]).toBe(`put /api/device-profiles/${AGG} aggregator`)
    expect(calls.slice(1).sort()).toEqual([`add ${DEV_A}`, `add ${DEV_B}`].sort())
    expect(pairs.get(DEV_A)).toBe(AGG)
  })

  it('shows a device an aggregator holds ticked, and unticking removes only that pair', async () => {
    serverKind = 'aggregator'
    await seedDeviceProfiles([{ lfdi: AGG, name: 'Agg', kind: 'aggregator' }])
    pairs.set(DEV_A, AGG)
    pairs.set(DEV_B, AGG)
    await edit('Agg')
    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    expect(screen.getByRole('checkbox', { name: '222222222222' })).toBeChecked()

    await fireEvent.click(screen.getByRole('checkbox', { name: '222222222222' }))
    await fireEvent.click(screen.getByTestId('device-name-save'))

    await waitFor(() => expect(screen.queryByTestId('device-name-input')).toBeNull())
    expect(calls).toEqual([`put /api/device-profiles/${AGG} aggregator`, `remove ${DEV_A}`])
    expect(pairs.has(DEV_B)).toBe(true)
  })

  it('shows the server 409 text and creates no pair when the kind is refused', async () => {
    refuseProfile = 'device is managed by 5555'
    await edit('Agg')
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'aggregator' } })
    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    await fireEvent.click(screen.getByRole('checkbox', { name: '222222222222' }))
    await fireEvent.click(screen.getByTestId('device-name-save'))

    expect(await screen.findByTestId('device-rename-error')).toHaveTextContent('Save failed (409): device is managed by 5555')
    expect(calls).toEqual([`put /api/device-profiles/${AGG} aggregator`])
  })

  it('names a refused pair by its device and keeps the saved kind', async () => {
    vi.mocked(api.postJSON).mockResolvedValue({ ok: false, error: 'device already has a different manager', status: 409 })
    await edit('Agg')
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'aggregator' } })
    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    await fireEvent.click(screen.getByRole('checkbox', { name: '222222222222' }))
    await fireEvent.click(screen.getByTestId('device-name-save'))

    expect(await screen.findByTestId('device-pair-error')).toHaveTextContent(
      'Pair failed for 222222222222: device already has a different manager (409)',
    )
    expect(calls).toEqual([`put /api/device-profiles/${AGG} aggregator`])
  })

  it('keeps a plain rename a name-only save, with the kind read from the server', async () => {
    await edit('Agg')
    expect(screen.queryByTestId('device-picker')).toBeNull()
    await fireEvent.input(screen.getByTestId('device-name-input'), { target: { value: 'Roof' } })
    await fireEvent.click(screen.getByTestId('device-name-save'))

    await waitFor(() => expect(screen.queryByTestId('device-name-input')).toBeNull())
    expect(calls).toEqual([`put /api/device-profiles/${AGG} device`])
  })
})

describe('Devices tab Edit: picker lifetime', () => {
  const failPairsRead = () => {
    const live = vi.mocked(api.fetchJSON).getMockImplementation()!
    vi.mocked(api.fetchJSON).mockImplementation(async (path: string, o) =>
      path.startsWith('/api/management-pairs') ? { ok: false, error: 'pairs down', status: 500 } : live(path, o),
    )
  }

  it('renames an aggregator when the pair list cannot be read', async () => {
    serverKind = 'aggregator'
    await seedDeviceProfiles([{ lfdi: AGG, name: 'Agg', kind: 'aggregator' }])
    failPairsRead()
    await edit('Agg')
    await fireEvent.input(screen.getByTestId('device-name-input'), { target: { value: 'Roof' } })
    const save = screen.getByTestId('device-name-save')
    expect(save).toBeEnabled()
    await fireEvent.click(save)

    await waitFor(() => expect(screen.queryByTestId('device-name-input')).toBeNull())
    expect(calls).toEqual([`put /api/device-profiles/${AGG} aggregator`])
  })

  it('keeps unsaved ticks when the kind is switched to Device and back', async () => {
    await edit('Agg')
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'aggregator' } })
    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    await fireEvent.click(screen.getByRole('checkbox', { name: '222222222222' }))
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'device' } })
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'aggregator' } })

    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    expect(screen.getByRole('checkbox', { name: '222222222222' })).toBeChecked()
  })

  it('keeps the held pairs visible after switching to Device, and removes them before the kind change', async () => {
    serverKind = 'aggregator'
    await seedDeviceProfiles([{ lfdi: AGG, name: 'Agg', kind: 'aggregator' }])
    pairs.set(DEV_A, AGG)
    await edit('Agg')
    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'device' } })

    const box = screen.getByRole('checkbox', { name: '222222222222' })
    expect(box).toBeChecked()
    expect(screen.queryByRole('checkbox', { name: '333333333333' })).toBeNull()
    await fireEvent.click(box)
    await fireEvent.click(screen.getByTestId('device-name-save'))

    await waitFor(() => expect(screen.queryByTestId('device-name-input')).toBeNull())
    expect(calls).toEqual([`remove ${DEV_A}`, `put /api/device-profiles/${AGG} device`])
  })
})

describe('Devices tab Edit: failed kind change after removals', () => {
  it('reloads the held pairs and names the removed ones, so a second Save does not remove again', async () => {
    serverKind = 'aggregator'
    await seedDeviceProfiles([{ lfdi: AGG, name: 'Agg', kind: 'aggregator' }])
    pairs.set(DEV_A, AGG)
    refuseProfile = 'device manages other devices'
    await edit('Agg')
    await waitFor(() => expect(screen.getAllByTestId('device-picker-option')).toHaveLength(2))
    await fireEvent.change(screen.getByTestId('device-kind-select'), { target: { value: 'device' } })
    await fireEvent.click(screen.getByRole('checkbox', { name: '222222222222' }))
    await fireEvent.click(screen.getByTestId('device-name-save'))

    const err = await screen.findByTestId('device-rename-error')
    expect(err).toHaveTextContent('Save failed (409): device manages other devices')
    expect(err).toHaveTextContent('Already removed: 222222222222')
    await waitFor(() => expect(screen.queryByTestId('device-picker')).toBeNull())

    await fireEvent.click(screen.getByTestId('device-name-save'))
    await waitFor(() => expect(calls.filter((c) => c.startsWith('put'))).toHaveLength(2))
    expect(calls.filter((c) => c.startsWith('remove'))).toEqual([`remove ${DEV_A}`])
  })
})
