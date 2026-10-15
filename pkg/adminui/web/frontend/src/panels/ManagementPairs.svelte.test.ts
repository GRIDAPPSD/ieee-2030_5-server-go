// The pairs panel decides which aggregator may read which devices, so each
// test asserts the request the server receives (path, query, body, case of
// the LFDI) and the rows the panel shows afterwards, against a fake
// /api/management-pairs that keeps real state.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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

async function choose(label: string, lfdi: string) {
  await fireEvent.change(screen.getByLabelText(label), { target: { value: lfdi } })
}

describe('ManagementPairs', () => {
  it('lists the pairs the server holds for the registered devices', async () => {
    pairs.set(DEV_A, AGG)
    pairs.set(DEV_B, AGG)

    render(ManagementPairs, { props: { devices } })

    const rows = await screen.findAllByTestId('pair-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByTestId('pair-manager')).toHaveTextContent(AGG)
    expect(within(rows[0]).getByTestId('pair-managed')).toHaveTextContent(DEV_A)
    expect(within(rows[1]).getByTestId('pair-managed')).toHaveTextContent(DEV_B)
  })

  it('shows an empty state when no pair exists', async () => {
    render(ManagementPairs, { props: { devices } })

    expect(await screen.findByTestId('pairs-empty')).toBeInTheDocument()
    expect(screen.queryAllByTestId('pair-row')).toHaveLength(0)
  })

  it('offers only registered devices in both selects', async () => {
    render(ManagementPairs, { props: { devices } })
    await screen.findByTestId('pairs-empty')

    for (const label of ['Manager', 'Managed device']) {
      const values = within(screen.getByLabelText(label))
        .getAllByRole('option')
        .map((o) => (o as HTMLOptionElement).value)
      expect(values).toEqual(['', AGG, DEV_A, DEV_B])
    }
  })

  it('posts the chosen pair and lists it afterwards', async () => {
    render(ManagementPairs, { props: { devices } })
    await screen.findByTestId('pairs-empty')

    await choose('Manager', AGG)
    await choose('Managed device', DEV_A)
    await fireEvent.click(screen.getByRole('button', { name: 'Create pair' }))

    await waitFor(() => expect(api.postJSON).toHaveBeenCalledTimes(1))
    expect(api.postJSON).toHaveBeenCalledWith('/api/management-pairs', { managerLFDI: AGG, managedLFDI: DEV_A })
    const row = await screen.findByTestId('pair-row')
    expect(within(row).getByTestId('pair-manager')).toHaveTextContent(AGG)
    expect(within(row).getByTestId('pair-managed')).toHaveTextContent(DEV_A)
    expect(screen.getByTestId('pairs-result')).toHaveTextContent('Paired')
  })

  it('shows the 409 for a device another manager holds as the server gave it, and lists no new pair', async () => {
    pairs.set(DEV_A, DEV_B)
    refuseCreate = { status: 409, error: 'device already has a different manager' }

    render(ManagementPairs, { props: { devices } })
    await screen.findByTestId('pair-row')

    await choose('Manager', AGG)
    await choose('Managed device', DEV_A)
    await fireEvent.click(screen.getByRole('button', { name: 'Create pair' }))

    const result = await screen.findByTestId('pairs-result')
    expect(result).toHaveTextContent('Error (409): device already has a different manager')
    expect(result).toHaveAttribute('role', 'alert')
    const rows = screen.getAllByTestId('pair-row')
    expect(rows).toHaveLength(1)
    expect(within(rows[0]).getByTestId('pair-manager')).toHaveTextContent(DEV_B)
  })

  it('refuses a device paired with itself without posting', async () => {
    render(ManagementPairs, { props: { devices } })
    await screen.findByTestId('pairs-empty')

    await choose('Manager', AGG)
    await choose('Managed device', AGG)
    await fireEvent.click(screen.getByRole('button', { name: 'Create pair' }))

    expect(await screen.findByTestId('pairs-result')).toHaveTextContent('A device cannot manage itself.')
    expect(api.postJSON).not.toHaveBeenCalled()
  })

  it('refuses a missing choice without posting', async () => {
    render(ManagementPairs, { props: { devices } })
    await screen.findByTestId('pairs-empty')

    await choose('Manager', AGG)
    await fireEvent.click(screen.getByRole('button', { name: 'Create pair' }))

    expect(await screen.findByTestId('pairs-result')).toHaveTextContent('Choose a manager and a managed device.')
    expect(api.postJSON).not.toHaveBeenCalled()
  })

  it('removes a pair by its managed LFDI and drops the row', async () => {
    pairs.set(DEV_A, AGG)
    pairs.set(DEV_B, AGG)

    render(ManagementPairs, { props: { devices } })
    const rows = await screen.findAllByTestId('pair-row')
    await fireEvent.click(within(rows[0]).getByRole('button', { name: 'Remove' }))

    await waitFor(() => expect(api.deleteJSON).toHaveBeenCalledTimes(1))
    expect(api.deleteJSON).toHaveBeenCalledWith(`/api/management-pairs?managed=${DEV_A}`)
    await waitFor(() => expect(screen.getAllByTestId('pair-row')).toHaveLength(1))
    expect(screen.getByTestId('pair-managed')).toHaveTextContent(DEV_B)
    expect(pairs.has(DEV_A)).toBe(false)
    expect(pairs.get(DEV_B)).toBe(AGG)
  })

  it('shows a failed removal as given and keeps the row', async () => {
    pairs.set(DEV_A, AGG)
    render(ManagementPairs, { props: { devices } })
    const row = await screen.findByTestId('pair-row')
    pairs.clear()

    await fireEvent.click(within(row).getByRole('button', { name: 'Remove' }))

    expect(await screen.findByTestId('pairs-result')).toHaveTextContent('Error (404): device is not managed')
  })

  it('reports a list failure instead of an empty table', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, error: 'list: request failed, see server log', status: 500 })

    render(ManagementPairs, { props: { devices } })

    expect(await screen.findByTestId('pairs-list-error')).toHaveTextContent('list: request failed, see server log')
    expect(screen.queryByTestId('pairs-empty')).not.toBeInTheDocument()
  })
})
