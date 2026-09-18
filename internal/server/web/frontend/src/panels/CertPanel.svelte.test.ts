// Two properties matter here beyond "the form posts": the CA download must
// save a PEM rather than the JSON envelope the route actually returns, and
// the issued device key must be delivered rather than received and dropped.
// POST /api/certs/server is asserted to be unreachable from this panel.
// Device types come from GET /api/certs/device-types (issue 594): every
// test mocks it, and each test touching the select or Generate waits for
// the fetch to settle first, the same way any effect-driven test does.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import CertPanel from './CertPanel.svelte'
import * as api from '../lib/api'
import * as dl from '../lib/download'

const CERT_PEM = '-----BEGIN CERTIFICATE-----\nMIIBdevice\n-----END CERTIFICATE-----\n'
const KEY_PEM = '-----BEGIN EC PRIVATE KEY-----\nMHcCAQEE\n-----END EC PRIVATE KEY-----\n'
const CA_PEM = '-----BEGIN CERTIFICATE-----\nMIIBca\n-----END CERTIFICATE-----\n'

const DEVICE_TYPES = [
  { value: 1, name: 'generic', label: 'Generic' },
  { value: 2, name: 'mobile', label: 'Mobile' },
  { value: 3, name: 'post_manufacture', label: 'Post-Manufacture' },
]

// Every fetchJSON call in this component goes through one spy, so a test
// that also mocks the CA route (or wants the device-types call to fail)
// dispatches on path; an unlisted path fails loudly rather than falling
// through to a mismatched shape.
function mockFetchJSON(overrides: Record<string, { ok: true; data: unknown } | { ok: false; error: string; status: number }> = {}) {
  return vi.spyOn(api, 'fetchJSON').mockImplementation((async (path: string) => {
    if (path in overrides) return overrides[path]
    if (path === '/api/certs/device-types') return { ok: true, data: { deviceTypes: DEVICE_TYPES } }
    throw new Error(`CertPanel.svelte.test.ts: unmocked fetchJSON(${path})`)
  }) as typeof api.fetchJSON)
}

async function waitReady(container: HTMLElement) {
  await waitFor(() => {
    expect(container.querySelector('#deviceType')).not.toBeDisabled()
  })
}

describe('CertPanel', () => {
  it('posts the typed hardware serial and renders the SFDI the server derived', async () => {
    mockFetchJSON()
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '167261211635', lfdi: 'ABC' },
    })

    const { container } = render(CertPanel)
    await waitReady(container)
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
    mockFetchJSON()
    vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '1', lfdi: 'A' },
    })
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    const { container } = render(CertPanel)
    await waitReady(container)
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
    mockFetchJSON()
    vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '1', lfdi: 'A' },
    })

    const { container } = render(CertPanel)
    await waitReady(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await screen.findByTestId('download-device-key')
    expect(container.innerHTML).not.toContain('PRIVATE KEY')
    expect(container.innerHTML).not.toContain('MHcCAQEE')
  })

  it('offers no download until a certificate has been issued', async () => {
    mockFetchJSON()
    const { container } = render(CertPanel)
    await waitReady(container)

    expect(screen.queryByTestId('download-device-cert')).toBeNull()
    expect(screen.queryByTestId('download-device-key')).toBeNull()
  })

  it('reports a device cert failure rather than leaving the result blank', async () => {
    mockFetchJSON()
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, error: 'no CA loaded', status: 503 })

    const { container } = render(CertPanel)
    await waitReady(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => {
      expect(container.querySelector('#certResult')).toHaveTextContent('Error: no CA loaded')
    })
    expect(screen.queryByTestId('download-device-key')).toBeNull()
  })

  it('saves the CA as a PEM file, not as the JSON envelope the route returns', async () => {
    const get = mockFetchJSON({ '/api/certs/ca': { ok: true, data: { certPEM: CA_PEM } } })
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    const { container } = render(CertPanel)
    await waitReady(container)
    await fireEvent.click(screen.getByTestId('download-ca'))

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1))
    expect(get).toHaveBeenCalledWith('/api/certs/ca')
    expect(save).toHaveBeenCalledWith('ca.crt', CA_PEM, 'application/x-pem-file')
    expect(screen.getByTestId('ca-result')).toHaveTextContent('Saved ca.crt.')
  })

  it('refuses to save an empty CA rather than writing a zero-byte trust anchor', async () => {
    mockFetchJSON({ '/api/certs/ca': { ok: true, data: { certPEM: '' } } })
    const save = vi.spyOn(dl, 'downloadText').mockImplementation(() => {})

    const { container } = render(CertPanel)
    await waitReady(container)
    await fireEvent.click(screen.getByTestId('download-ca'))

    await waitFor(() => {
      expect(screen.getByTestId('ca-result')).toHaveTextContent('The server returned no CA certificate.')
    })
    expect(save).not.toHaveBeenCalled()
  })

  it('reports a CA download failure', async () => {
    mockFetchJSON({ '/api/certs/ca': { ok: false, error: 'CA not initialized', status: 503 } })

    const { container } = render(CertPanel)
    await waitReady(container)
    await fireEvent.click(screen.getByTestId('download-ca'))

    await waitFor(() => {
      expect(screen.getByTestId('ca-result')).toHaveTextContent('Error: CA not initialized')
    })
  })

  it('sends the selected device type, not the Generic default, once the operator changes it', async () => {
    mockFetchJSON()
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '167261211635', lfdi: 'ABC' },
    })

    const { container } = render(CertPanel)
    await waitReady(container)
    await fireEvent.change(container.querySelector('#deviceType') as HTMLSelectElement, {
      target: { value: '2' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/certs/device', {
      deviceType: 2,
      hwSerialNum: '',
      hwType: '',
    })
  })

  it('carries a successful mint to Add End Device with the identifiers the server derived', async () => {
    mockFetchJSON()
    vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '167261211635', lfdi: 'ABCDEF' },
    })
    const onMinted = vi.fn()

    const { container } = render(CertPanel, { props: { onMinted } })
    await waitReady(container)
    await fireEvent.input(container.querySelector('#hwSerial') as HTMLInputElement, {
      target: { value: 'PW-INV-002' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => expect(onMinted).toHaveBeenCalledTimes(1))
    expect(onMinted).toHaveBeenCalledWith({
      certPEM: CERT_PEM,
      keyPEM: KEY_PEM,
      sfdi: '167261211635',
      lfdi: 'ABCDEF',
      serial: 'PW-INV-002',
    })
  })

  it('does not carry a failed mint: onMinted is not called on error', async () => {
    mockFetchJSON()
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, error: 'no CA loaded', status: 503 })
    const onMinted = vi.fn()

    const { container } = render(CertPanel, { props: { onMinted } })
    await waitReady(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => {
      expect(container.querySelector('#certResult')).toHaveTextContent('Error: no CA loaded')
    })
    expect(onMinted).not.toHaveBeenCalled()
  })

  it('never posts to the server-cert route: that key would arrive undelivered', async () => {
    // This test clicks every button in the card, "Download CA certificate"
    // included, so the CA route needs a response too.
    mockFetchJSON({ '/api/certs/ca': { ok: true, data: { certPEM: CA_PEM } } })
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { certPEM: CERT_PEM, keyPEM: KEY_PEM, sfdi: '1', lfdi: 'A' },
    })

    const { container } = render(CertPanel)
    await waitReady(container)
    for (const button of Array.from(container.querySelectorAll('button'))) {
      await fireEvent.click(button)
    }

    const paths = post.mock.calls.map(([path]) => path)
    expect(paths).not.toContain('/api/certs/server')
    expect(container.querySelector('#serverCertHosts')).toBeNull()
  })

  it('populates the select from the response, not from a hardcoded list', async () => {
    mockFetchJSON()
    const { container } = render(CertPanel)
    await waitReady(container)

    const options = Array.from(container.querySelectorAll('#deviceType option')).map((opt) => ({
      value: (opt as HTMLOptionElement).value,
      label: opt.textContent,
    }))
    expect(options).toEqual([
      { value: '1', label: 'Generic' },
      { value: '2', label: 'Mobile' },
      { value: '3', label: 'Post-Manufacture' },
    ])
  })

  it('disables the select and Generate, and explains why, when the fetch fails', async () => {
    mockFetchJSON({ '/api/certs/device-types': { ok: false, error: 'service unavailable', status: 503 } })

    const { container } = render(CertPanel)

    await waitFor(() => {
      expect(screen.getByTestId('device-types-error')).toHaveTextContent(
        'Could not load device types: service unavailable. Reload the page to try again.',
      )
    })
    expect(container.querySelector('#deviceType')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Generate Device Cert' })).toBeDisabled()
    expect(screen.queryByTestId('device-types-loading')).toBeNull()
  })
})
