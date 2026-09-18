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
      description: 'roof inverter',
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
      description: '',
      pin: 9000,
      enabled: true,
    })
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
