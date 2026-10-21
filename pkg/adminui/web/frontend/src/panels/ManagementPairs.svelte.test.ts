// The pairs panel decides which aggregator may read which devices, so each
// test asserts the request the server receives (path, query, body, case of
// the LFDI) and the rows the panel shows afterwards, against a fake
// /api/management-pairs that keeps real state.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { tick } from 'svelte'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import ManagementPairs from './ManagementPairs.svelte'
import * as api from '../lib/api'
import type { DashboardDevice } from '../lib/dashboard'

const AGG = '0123456789ABCDEF0123456789ABCDEF01234567'
const DEV_A = 'FEDCBA9876543210FEDCBA9876543210FEDCBA98'
const DEV_B = '5555555555555555555555555555555555555555'

function device(sfdi: string, lfdi: string, n: number): DashboardDevice {
  return {
    sfdi,
    lfdi,
    href: `/edev/${n}`,
    enabled: true,
    lastRequest: null,
    comms: 'not_seen',
    lastKnown: false,
    ders: [],
  }
}

const devices = [device('111111111111', AGG, 1), device('222222222222', DEV_A, 2), device('333333333333', DEV_B, 3)]

// managed LFDI -> manager LFDI, as the server's store holds it.
let pairs: Map<string, string>
let refuseCreate: { status: number; error: string } | null

beforeEach(() => {
  pairs = new Map()
  refuseCreate = null

  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    const url = new URL(path, 'http://x')
    const manager = url.searchParams.get('manager')
    if (url.pathname !== '/api/management-pairs' || manager === null) {
      return { ok: false, error: 'unexpected request', status: 500 }
    }
    const managed = [...pairs].filter(([, m]) => m === manager).map(([d]) => d)
    return { ok: true, data: { managerLFDI: manager, managedLFDIs: managed } } as never
  })
  vi.spyOn(api, 'postJSON').mockImplementation(async (path: string, body: unknown) => {
    const req = body as { managerLFDI: string; managedLFDI: string }
    if (path !== '/api/management-pairs') return { ok: false, error: 'unexpected request', status: 500 }
    if (refuseCreate) return { ok: false, ...refuseCreate }
    pairs.set(req.managedLFDI, req.managerLFDI)
    return { ok: true, data: req } as never
  })
  vi.spyOn(api, 'deleteJSON').mockImplementation(async (path: string) => {
    const managed = new URL(path, 'http://x').searchParams.get('managed')
    if (managed === null || !pairs.delete(managed)) return { ok: false, error: 'device is not managed', status: 404 }
    return { ok: true, data: undefined as never }
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ManagementPairs', () => {
  it('lists the pairs the server holds for the registered devices', async () => {
    pairs.set(DEV_A, AGG)
    pairs.set(DEV_B, AGG)

    render(ManagementPairs, { props: { devices, ready: true } })

    const rows = await screen.findAllByTestId('pair-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByTestId('pair-manager')).toHaveTextContent(AGG)
    expect(within(rows[0]).getByTestId('pair-managed')).toHaveTextContent(DEV_A)
    expect(within(rows[1]).getByTestId('pair-managed')).toHaveTextContent(DEV_B)
  })

  it('has no create form: pairs are made from the device forms', async () => {
    render(ManagementPairs, { props: { devices, ready: true } })
    await screen.findByTestId('pairs-empty')

    expect(screen.queryByRole('button', { name: 'Create pair' })).toBeNull()
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  it('shows an empty state when no pair exists', async () => {
    render(ManagementPairs, { props: { devices, ready: true } })

    expect(await screen.findByTestId('pairs-empty')).toBeInTheDocument()
    expect(screen.queryAllByTestId('pair-row')).toHaveLength(0)
  })

  it('removes a pair by its managed LFDI and drops the row', async () => {
    pairs.set(DEV_A, AGG)
    pairs.set(DEV_B, AGG)

    render(ManagementPairs, { props: { devices, ready: true } })
    const rows = await screen.findAllByTestId('pair-row')
    await fireEvent.click(within(rows[0]).getByRole('button', { name: /^Remove pair for/ }))

    await waitFor(() => expect(api.deleteJSON).toHaveBeenCalledTimes(1))
    expect(api.deleteJSON).toHaveBeenCalledWith(`/api/management-pairs?managed=${DEV_A}`)
    await waitFor(() => expect(screen.getAllByTestId('pair-row')).toHaveLength(1))
    expect(screen.getByTestId('pair-managed')).toHaveTextContent(DEV_B)
    expect(pairs.has(DEV_A)).toBe(false)
    expect(pairs.get(DEV_B)).toBe(AGG)
  })

  it('shows a failed removal as given and keeps the row', async () => {
    pairs.set(DEV_A, AGG)
    render(ManagementPairs, { props: { devices, ready: true } })
    const row = await screen.findByTestId('pair-row')
    pairs.clear()

    await fireEvent.click(within(row).getByRole('button', { name: /^Remove pair for/ }))

    expect(await screen.findByTestId('pairs-result')).toHaveTextContent('Error (404): device is not managed')
    const rows = screen.getAllByTestId('pair-row')
    expect(rows).toHaveLength(1)
    expect(within(rows[0]).getByTestId('pair-managed')).toHaveTextContent(DEV_A)
  })

  it('reports a list failure instead of an empty table', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, error: 'list: request failed, see server log', status: 500 })

    render(ManagementPairs, { props: { devices, ready: true } })

    expect(await screen.findByTestId('pairs-list-error')).toHaveTextContent('list: request failed, see server log')
    expect(screen.queryByTestId('pairs-empty')).not.toBeInTheDocument()
  })

  it('shows a loading state, not the empty state, until the device list has arrived', async () => {
    const { rerender } = render(ManagementPairs, { props: { devices: [], ready: false } })
    await tick()

    expect(screen.getByTestId('pairs-loading')).toBeInTheDocument()
    expect(screen.queryByTestId('pairs-empty')).not.toBeInTheDocument()
    expect(api.fetchJSON).not.toHaveBeenCalled()

    pairs.set(DEV_A, AGG)
    await rerender({ devices, ready: true })

    expect(await screen.findByTestId('pair-row')).toBeInTheDocument()
    expect(screen.queryByTestId('pairs-loading')).not.toBeInTheDocument()
  })

  it('shows the empty state for a ready server with no devices', async () => {
    render(ManagementPairs, { props: { devices: [], ready: true } })

    expect(await screen.findByTestId('pairs-empty')).toBeInTheDocument()
  })

  it('reloads only when the set of LFDIs changes, not on a reorder or a case change', async () => {
    const { rerender } = render(ManagementPairs, { props: { devices: [devices[0], devices[1]], ready: true } })
    await screen.findByTestId('pairs-empty')
    expect(api.fetchJSON).toHaveBeenCalledTimes(2)

    const lower = (d: DashboardDevice) => ({ ...d, lfdi: d.lfdi.toLowerCase() })
    await rerender({ devices: [lower(devices[1]), lower(devices[0])], ready: true })
    await tick()
    await Promise.resolve()
    expect(api.fetchJSON).toHaveBeenCalledTimes(2)

    await rerender({ devices: [devices[0], devices[1], devices[2]], ready: true })
    await waitFor(() => expect(api.fetchJSON).toHaveBeenCalledTimes(5))
  })

  it('drops a list answer that a newer load has replaced', async () => {
    pairs.set(DEV_A, AGG)
    let release!: (v: Awaited<ReturnType<typeof api.fetchJSON>>) => void
    const held = new Promise<Awaited<ReturnType<typeof api.fetchJSON>>>((r) => (release = r))
    const live = vi.mocked(api.fetchJSON).getMockImplementation()!
    vi.mocked(api.fetchJSON).mockImplementationOnce(() => held)
    vi.mocked(api.fetchJSON).mockImplementation(live)

    const { rerender } = render(ManagementPairs, { props: { devices: [devices[0]], ready: true } })
    await rerender({ devices: [devices[0], devices[1]], ready: true })
    expect(await screen.findByTestId('pair-row')).toBeInTheDocument()

    release({ ok: true, data: { managerLFDI: AGG, managedLFDIs: [] } } as never)
    for (let i = 0; i < 5; i++) await tick()

    const rows = screen.getAllByTestId('pair-row')
    expect(rows).toHaveLength(1)
    expect(within(rows[0]).getByTestId('pair-managed')).toHaveTextContent(DEV_A)
  })

  it('sends one remove request while the first is pending and disables the buttons', async () => {
    pairs.set(DEV_A, AGG)
    let release!: () => void
    const held = new Promise<void>((r) => (release = r))
    const live = vi.mocked(api.deleteJSON).getMockImplementation()!
    vi.mocked(api.deleteJSON).mockImplementation(async (path) => {
      await held
      return live(path)
    })

    render(ManagementPairs, { props: { devices, ready: true } })
    const button = await screen.findByRole('button', { name: /^Remove pair for/ })
    await fireEvent.click(button)
    await fireEvent.click(button)

    expect(button).toBeDisabled()
    expect(api.deleteJSON).toHaveBeenCalledTimes(1)
    release()
    await waitFor(() => expect(screen.queryAllByTestId('pair-row')).toHaveLength(0))
    expect(api.deleteJSON).toHaveBeenCalledTimes(1)
  })

  it('names each Remove button for the device it removes', async () => {
    pairs.set(DEV_A, AGG)
    pairs.set(DEV_B, AGG)

    render(ManagementPairs, { props: { devices, ready: true } })

    expect(await screen.findByRole('button', { name: `Remove pair for ${DEV_A}` })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: `Remove pair for ${DEV_B}` })).toBeInTheDocument()
  })

})
