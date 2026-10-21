// The identity assertions this panel exists for: the SFDI and LFDI the
// server derived from the certificate must appear in the form unchanged,
// and must be the values POSTed to /api/devices. A registration keyed off
// a mangled or defaulted LFDI produces a device the real client can never
// match.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AddDevice from './AddDevice.svelte'
import * as api from '../lib/api'
import * as dl from '../lib/download'
import type { MintedCert } from '../lib/deviceCert'

const PEM = '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n'
const SFDI = '167261211635'
const LFDI = '3E4F45AB31EDFE5B67E343E5E4562E31984E23E5'

const MINTED: MintedCert = {
  certPEM: '-----BEGIN CERTIFICATE-----\nMIIBmint\n-----END CERTIFICATE-----\n',
  keyPEM: '-----BEGIN EC PRIVATE KEY-----\nMHcCmint\n-----END EC PRIVATE KEY-----\n',
  sfdi: '211635167261',
  lfdi: '5B67E343E5E4562E31984E23E3E4F45AB31EDFE',
  serial: 'PW-INV-003',
}

describe('AddDevice', () => {
  it('fills the readonly SFDI and LFDI fields with exactly the values the cert parser returned', async () => {
    vi.spyOn(api, 'postBody').mockResolvedValue({
      ok: true,
      data: { sfdi: SFDI, lfdi: LFDI, subject: 'CN=e2e-test-device' },
    })

    const { container } = render(AddDevice)

    await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, {
      target: { value: PEM },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))

    await waitFor(() => {
      expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe(SFDI)
    })
    expect((container.querySelector('#addDevLFDI') as HTMLInputElement).value).toBe(LFDI)
    expect(container.querySelector('#addDevResult')).toHaveTextContent(`SFDI=${SFDI}`)
    expect(container.querySelector('#addDevResult')).toHaveTextContent('Subject="CN=e2e-test-device"')
  })

  it('posts the parsed identity and the typed PIN as a number, and reports the created href', async () => {
    vi.spyOn(api, 'postBody').mockResolvedValue({
      ok: true,
      data: { sfdi: SFDI, lfdi: LFDI, subject: 'CN=dev' },
    })
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { href: '/edev/7', sfdi: SFDI, lfdi: LFDI },
    })

    const { container } = render(AddDevice)

    await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, {
      target: { value: PEM },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))
    await waitFor(() => {
      expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe(SFDI)
    })

    await fireEvent.input(container.querySelector('#addDevDesc') as HTMLInputElement, {
      target: { value: 'roof inverter' },
    })
    await fireEvent.input(container.querySelector('#addDevPIN') as HTMLInputElement, {
      target: { value: '424242' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/devices', {
      sfdi: SFDI,
      lfdi: LFDI,
      name: 'roof inverter',
      kind: 'device',
      pin: 424242,
      enabled: true,
    })
    expect(container.querySelector('#addDevResult')).toHaveTextContent('Created /edev/7 (PIN persisted).')
  })

  it('refuses to register before a cert is parsed rather than posting a blank identity', async () => {
    const post = vi.spyOn(api, 'postJSON')

    const { container } = render(AddDevice)
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => {
      expect(container.querySelector('#addDevResult')).toHaveTextContent(
        'Parse a cert first (SFDI + LFDI required).',
      )
    })
    expect(post).not.toHaveBeenCalled()
  })

  it('refuses a missing PIN rather than registering a device with no registration PIN', async () => {
    vi.spyOn(api, 'postBody').mockResolvedValue({
      ok: true,
      data: { sfdi: SFDI, lfdi: LFDI, subject: 'CN=dev' },
    })
    const post = vi.spyOn(api, 'postJSON')

    const { container } = render(AddDevice)
    await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, {
      target: { value: PEM },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))
    await waitFor(() => {
      expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe(SFDI)
    })

    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => {
      expect(container.querySelector('#addDevResult')).toHaveTextContent(
        'PIN must be a non-negative integer.',
      )
    })
    expect(post).not.toHaveBeenCalled()
  })

  it('refuses a body that is not a certificate without calling the parser', async () => {
    const parse = vi.spyOn(api, 'postBody')

    const { container } = render(AddDevice)
    await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, {
      target: { value: 'not a cert' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))

    await waitFor(() => {
      expect(container.querySelector('#addDevResult')).toHaveTextContent('Paste a PEM certificate first.')
    })
    expect(parse).not.toHaveBeenCalled()
  })

  it('pre-fills the readonly SFDI and LFDI fields from a carried mint, with no Parse Cert click', async () => {
    const { container } = render(AddDevice, { props: { pending: MINTED } })

    await waitFor(() => {
      expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe(MINTED.sfdi)
    })
    expect((container.querySelector('#addDevLFDI') as HTMLInputElement).value).toBe(MINTED.lfdi)
  })

  it('registers a carried mint with exactly the identifiers it arrived with', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { href: '/edev/9', sfdi: MINTED.sfdi, lfdi: MINTED.lfdi },
    })

    const { container } = render(AddDevice, { props: { pending: MINTED } })
    await waitFor(() => {
      expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe(MINTED.sfdi)
    })

    await fireEvent.input(container.querySelector('#addDevPIN') as HTMLInputElement, {
      target: { value: '9000' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/devices', {
      sfdi: MINTED.sfdi,
      lfdi: MINTED.lfdi,
      name: '',
      kind: 'device',
      pin: 9000,
      enabled: true,
    })
  })

  it('keeps a carried mint visible and downloadable after a failed registration, so the credential is not lost track of', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, error: 'pin already in use', status: 409 })
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    const { container } = render(AddDevice, { props: { pending: MINTED } })
    await waitFor(() => {
      expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe(MINTED.sfdi)
    })

    await fireEvent.input(container.querySelector('#addDevPIN') as HTMLInputElement, {
      target: { value: '9000' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => {
      expect(container.querySelector('#addDevResult')).toHaveTextContent('Error: pin already in use')
    })
    // The identifiers and the download affordance both survive the
    // failure: the operator can fix the PIN and retry without the minted
    // key becoming unreachable.
    expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe(MINTED.sfdi)
    await fireEvent.click(screen.getByTestId('download-pending-key'))
    expect(save).toHaveBeenCalledWith('PW-INV-003.key', MINTED.keyPEM, 'application/x-pem-file')
  })

  it('offers no pending-mint download until a mint has been carried', () => {
    render(AddDevice)

    expect(screen.queryByTestId('pending-mint')).toBeNull()
  })

  it('delivers the carried certificate and key as files, named for the serial', async () => {
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    render(AddDevice, { props: { pending: MINTED } })

    await fireEvent.click(await screen.findByTestId('download-pending-cert'))
    expect(save).toHaveBeenCalledWith('PW-INV-003.crt', MINTED.certPEM, 'application/x-pem-file')

    await fireEvent.click(screen.getByTestId('download-pending-key'))
    expect(save).toHaveBeenCalledWith('PW-INV-003.key', MINTED.keyPEM, 'application/x-pem-file')
  })
})

describe('AddDevice aggregator', () => {
  const AGG_LFDI = '0123456789ABCDEF0123456789ABCDEF01234567'
  const DEV_A = 'FEDCBA9876543210FEDCBA9876543210FEDCBA98'
  const DEV_B = '5555555555555555555555555555555555555555'
  const DEV_C = '7777777777777777777777777777777777777777'
  const mk = (sfdi: string, lfdi: string, n: number) => ({
    sfdi, lfdi, href: `/edev/${n}`, enabled: true, lastRequest: null, comms: 'not_seen' as const, lastKnown: false, ders: [],
  })
  const devices = [mk('111111111111', DEV_A, 1), mk('222222222222', DEV_B, 2), mk('333333333333', DEV_C, 3)]

  // The pair store the picker reads: DEV_C is held by another manager.
  function fakeServer(refuse: Record<string, string> = {}) {
    const order: string[] = []
    vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      const manager = new URL(path, 'http://x').searchParams.get('manager')
      return { ok: true, data: { managerLFDI: manager, managedLFDIs: manager === DEV_B ? [DEV_C] : [] } } as never
    })
    vi.spyOn(api, 'postBody').mockResolvedValue({ ok: true, data: { sfdi: SFDI, lfdi: AGG_LFDI, subject: 'CN=agg' } })
    vi.spyOn(api, 'postJSON').mockImplementation(async (path: string, body: unknown) => {
      const b = body as { managedLFDI?: string }
      if (path === '/api/devices') {
        order.push('device')
        return { ok: true, data: { href: '/edev/9', sfdi: SFDI, lfdi: AGG_LFDI } } as never
      }
      order.push('pair ' + b.managedLFDI)
      const error = refuse[b.managedLFDI ?? '']
      return error ? { ok: false, error, status: 409 } : ({ ok: true, data: body } as never)
    })
    return order
  }

  async function fill(container: HTMLElement) {
    await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, { target: { value: PEM } })
    await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))
    await waitFor(() => expect((container.querySelector('#addDevLFDI') as HTMLInputElement).value).toBe(AGG_LFDI))
    await fireEvent.input(container.querySelector('#addDevPIN') as HTMLInputElement, { target: { value: '1234' } })
  }

  async function chooseAggregator(container: HTMLElement) {
    await fireEvent.change(container.querySelector('#addDevKind') as HTMLSelectElement, { target: { value: 'aggregator' } })
    await screen.findByTestId('device-picker')
    await waitFor(() => expect(screen.queryAllByTestId('device-picker-option').length).toBeGreaterThan(0))
  }

  it('shows no picker for a device', () => {
    fakeServer()
    render(AddDevice, { props: { devices } })

    expect(screen.queryByTestId('device-picker')).toBeNull()
  })

  it('offers neither an aggregator nor a device another manager holds', async () => {
    fakeServer()
    const { container } = render(AddDevice, { props: { devices } })
    await chooseAggregator(container)

    const names = screen.getAllByRole('checkbox').map((c) => c.closest('label')?.textContent?.trim())
    expect(names).toContain('111111111111')
    expect(names).not.toContain('222222222222')
    expect(names).not.toContain('333333333333')
  })

  it('posts the aggregator, then one pair per picked device', async () => {
    const order = fakeServer()
    const { container } = render(AddDevice, { props: { devices: [devices[0], mk('444444444444', '9999999999999999999999999999999999999999', 4)] } })
    await fill(container)
    await chooseAggregator(container)
    for (const box of screen.getAllByRole('checkbox', { name: /^(111111111111|444444444444)$/ })) {
      await fireEvent.click(box)
    }
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => expect(order).toHaveLength(3))
    expect(order[0]).toBe('device')
    expect(order.slice(1).sort()).toEqual(['pair 9999999999999999999999999999999999999999', 'pair ' + DEV_A].sort())
    expect(api.postJSON).toHaveBeenCalledWith('/api/devices', expect.objectContaining({ kind: 'aggregator', lfdi: AGG_LFDI }))
    expect(api.postJSON).toHaveBeenCalledWith('/api/management-pairs', { managerLFDI: AGG_LFDI, managedLFDI: DEV_A })
    expect(container.querySelector('#addDevResult')).toHaveTextContent('Paired 2 of 2 devices.')
  })

  it('names a refused pair by its device and still reports the aggregator created', async () => {
    fakeServer({ [DEV_A]: 'device already has a different manager' })
    const { container } = render(AddDevice, { props: { devices } })
    await fill(container)
    await chooseAggregator(container)
    await fireEvent.click(screen.getByRole('checkbox', { name: '111111111111' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => expect(container.querySelector('#addDevResult')).toHaveTextContent('Pair failed for 111111111111'))
    const text = container.querySelector('#addDevResult')?.textContent ?? ''
    expect(text).toContain('Created /edev/9')
    expect(text).toContain('device already has a different manager (409)')
    expect(text).toContain('Paired 0 of 1 devices.')
  })

  it('sends no pair when the device itself is refused', async () => {
    fakeServer()
    vi.mocked(api.postJSON).mockImplementation(async (path: string) =>
      path === '/api/devices' ? { ok: false, error: 'name already in use', status: 409 } : ({ ok: true, data: {} } as never),
    )
    const { container } = render(AddDevice, { props: { devices } })
    await fill(container)
    await chooseAggregator(container)
    await fireEvent.click(screen.getByRole('checkbox', { name: '111111111111' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => expect(container.querySelector('#addDevResult')).toHaveTextContent('Error: name already in use'))
    expect(api.postJSON).toHaveBeenCalledTimes(1)
  })
})

describe('AddDevice submit discipline', () => {
  const AGG_LFDI = '0123456789ABCDEF0123456789ABCDEF01234567'
  const DEV_A = 'FEDCBA9876543210FEDCBA9876543210FEDCBA98'
  const DEV_B = '5555555555555555555555555555555555555555'
  const mk = (sfdi: string, lfdi: string, n: number) => ({
    sfdi, lfdi, href: `/edev/${n}`, enabled: true, lastRequest: null, comms: 'not_seen' as const, lastKnown: false, ders: [],
  })
  const devA = mk('111111111111', DEV_A, 1)
  const devB = mk('222222222222', DEV_B, 2)

  function setup() {
    vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      const manager = new URL(path, 'http://x').searchParams.get('manager')
      return { ok: true, data: { managerLFDI: manager, managedLFDIs: [] } } as never
    })
    vi.spyOn(api, 'postBody').mockResolvedValue({ ok: true, data: { sfdi: SFDI, lfdi: AGG_LFDI, subject: 'CN=agg' } })
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const post = vi.spyOn(api, 'postJSON').mockImplementation(async (path: string, body: unknown) => {
      if (path === '/api/devices') {
        await gate
        return { ok: true, data: { href: '/edev/9', sfdi: SFDI, lfdi: AGG_LFDI } } as never
      }
      return { ok: true, data: body } as never
    })
    return { post, release }
  }

  async function ready(container: HTMLElement) {
    await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, { target: { value: PEM } })
    await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))
    await waitFor(() => expect((container.querySelector('#addDevLFDI') as HTMLInputElement).value).toBe(AGG_LFDI))
    await fireEvent.input(container.querySelector('#addDevPIN') as HTMLInputElement, { target: { value: '1234' } })
    await fireEvent.change(container.querySelector('#addDevKind') as HTMLSelectElement, { target: { value: 'aggregator' } })
    await waitFor(() => expect(screen.queryAllByTestId('device-picker-option').length).toBeGreaterThan(0))
  }

  it('sends one device POST for two clicks and disables the button while it is pending', async () => {
    const { post, release } = setup()
    const { container } = render(AddDevice, { props: { devices: [devA] } })
    await ready(container)
    await fireEvent.click(screen.getByRole('checkbox', { name: '111111111111' }))
    const button = screen.getByRole('button', { name: /Add Device|Adding/ })
    await fireEvent.click(button)
    await fireEvent.click(button)

    expect(button).toBeDisabled()
    expect(container.querySelector('#addDevResult')).toHaveTextContent('Adding')
    release()
    await waitFor(() => expect(container.querySelector('#addDevResult')).toHaveTextContent('Paired 1 of 1'))
    expect(post.mock.calls.filter((c) => c[0] === '/api/devices')).toHaveLength(1)
    expect(post.mock.calls.filter((c) => c[0] === '/api/management-pairs')).toHaveLength(1)
  })

  it('drops a pick that is no longer offered, so it is not posted', async () => {
    const { post, release } = setup()
    release()
    const { container, rerender } = render(AddDevice, { props: { devices: [devA, devB] } })
    await ready(container)
    await fireEvent.click(screen.getByRole('checkbox', { name: '111111111111' }))
    await fireEvent.click(screen.getByRole('checkbox', { name: '222222222222' }))
    await rerender({ devices: [devB] })
    await waitFor(() => expect(screen.queryByRole('checkbox', { name: '111111111111' })).toBeNull())
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))

    await waitFor(() => expect(container.querySelector('#addDevResult')).toHaveTextContent('Paired 1 of 1'))
    const pairs = post.mock.calls.filter((c) => c[0] === '/api/management-pairs').map((c) => (c[1] as { managedLFDI: string }).managedLFDI)
    expect(pairs).toEqual([DEV_B])
  })

  it('clears the picks after a submit, so the next add posts no stale pair', async () => {
    const { post, release } = setup()
    release()
    const { container } = render(AddDevice, { props: { devices: [devA] } })
    await ready(container)
    await fireEvent.click(screen.getByRole('checkbox', { name: '111111111111' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))
    await waitFor(() => expect(container.querySelector('#addDevResult')).toHaveTextContent('Paired 1 of 1'))

    expect(screen.getByRole('checkbox', { name: '111111111111' })).not.toBeChecked()
    await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))
    await waitFor(() => expect(post.mock.calls.filter((c) => c[0] === '/api/devices')).toHaveLength(2))
    expect(post.mock.calls.filter((c) => c[0] === '/api/management-pairs')).toHaveLength(1)
  })
})
