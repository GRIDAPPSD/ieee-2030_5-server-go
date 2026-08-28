// Two properties matter here beyond "the form posts": the CA download must
// save a PEM rather than the JSON envelope the route actually returns, and
// the issued device key must be delivered rather than received and dropped.
// POST /api/certs/server is asserted to be unreachable from this panel.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import CertPanel from './CertPanel.svelte'
import * as api from '../lib/api'
import * as dl from '../lib/download'

const CERT_PEM = '-----BEGIN CERTIFICATE-----\nMIIBdevice\n-----END CERTIFICATE-----\n'
const KEY_PEM = '-----BEGIN EC PRIVATE KEY-----\nMHcCAQEE\n-----END EC PRIVATE KEY-----\n'
const CA_PEM = '-----BEGIN CERTIFICATE-----\nMIIBca\n-----END CERTIFICATE-----\n'

describe('CertPanel', () => {
  it('posts the typed hardware serial and renders the SFDI the server derived', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '167261211635', lfdi: 'ABC' },
    })

    const { container } = render(CertPanel)
    await fireEvent.input(container.querySelector('#hwSerial') as HTMLInputElement, {
      target: { value: 'PW-INV-001' },
    })
    await fireEvent.input(container.querySelector('#hwType') as HTMLInputElement, {
      target: { value: '1.3.6.1.4.1.40732.99' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => {
      expect(container.querySelector('#certResult')).toHaveTextContent('Generated! SFDI: 167261211635')
    })
    // hwType is asserted in the body because the route 400s without it: the
    // previous request shape, carried over from the page this replaced, was
    // rejected every time.
    expect(post).toHaveBeenCalledWith('/api/certs/device', {
      deviceType: 1,
      hwSerialNum: 'PW-INV-001',
      hwType: '1.3.6.1.4.1.40732.99',
    })
  })

  it('delivers the issued certificate and private key as files, named for the serial', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '1', lfdi: 'A' },
    })
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    const { container } = render(CertPanel)
    await fireEvent.input(container.querySelector('#hwSerial') as HTMLInputElement, {
      target: { value: 'PW-INV-001' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await fireEvent.click(await screen.findByTestId('download-device-cert'))
    expect(save).toHaveBeenCalledWith('PW-INV-001.crt', CERT_PEM, 'application/x-pem-file')

    await fireEvent.click(screen.getByTestId('download-device-key'))
    expect(save).toHaveBeenCalledWith('PW-INV-001.key', KEY_PEM, 'application/x-pem-file')
  })

  it('never renders the private key into the DOM', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '1', lfdi: 'A' },
    })

    const { container } = render(CertPanel)
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await screen.findByTestId('download-device-key')
    expect(container.innerHTML).not.toContain('PRIVATE KEY')
    expect(container.innerHTML).not.toContain('MHcCAQEE')
  })

  it('offers no download until a certificate has been issued', () => {
    render(CertPanel)

    expect(screen.queryByTestId('download-device-cert')).toBeNull()
    expect(screen.queryByTestId('download-device-key')).toBeNull()
  })

  it('reports a device cert failure rather than leaving the result blank', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, error: 'no CA loaded', status: 503 })

    const { container } = render(CertPanel)
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => {
      expect(container.querySelector('#certResult')).toHaveTextContent('Error: no CA loaded')
    })
    expect(screen.queryByTestId('download-device-key')).toBeNull()
  })

  it('saves the CA as a PEM file, not as the JSON envelope the route returns', async () => {
    const get = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: { certPEM: CA_PEM } })
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    render(CertPanel)
    await fireEvent.click(screen.getByTestId('download-ca'))

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1))
    expect(get).toHaveBeenCalledWith('/api/certs/ca')
    expect(save).toHaveBeenCalledWith('ca.crt', CA_PEM, 'application/x-pem-file')
    expect(screen.getByTestId('ca-result')).toHaveTextContent('Saved ca.crt.')
  })

  it('refuses to save an empty CA rather than writing a zero-byte trust anchor', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: { certPEM: '' } })
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    render(CertPanel)
    await fireEvent.click(screen.getByTestId('download-ca'))

    await waitFor(() => {
      expect(screen.getByTestId('ca-result')).toHaveTextContent('The server returned no CA certificate.')
    })
    expect(save).not.toHaveBeenCalled()
  })

  it('reports a CA download failure', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, error: 'CA not initialized', status: 503 })

    render(CertPanel)
    await fireEvent.click(screen.getByTestId('download-ca'))

    await waitFor(() => {
      expect(screen.getByTestId('ca-result')).toHaveTextContent('Error: CA not initialized')
    })
  })

  it('never posts to the server-cert route: that key would arrive undelivered', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '1', lfdi: 'A' },
    })

    const { container } = render(CertPanel)
    for (const button of Array.from(container.querySelectorAll('button'))) {
      await fireEvent.click(button)
    }

    const paths = post.mock.calls.map(([path]) => path)
    expect(paths).not.toContain('/api/certs/server')
    expect(container.querySelector('#serverCertHosts')).toBeNull()
  })
})
