import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import CertPanel from './CertPanel.svelte'
import * as api from '../lib/api'

describe('CertPanel', () => {
  it('posts the typed hardware serial and renders the SFDI the server derived', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { sfdi: '167261211635', lfdi: 'ABC' },
    })

    const { container } = render(CertPanel)
    await fireEvent.input(container.querySelector('#hwSerial') as HTMLInputElement, {
      target: { value: 'PW-INV-001' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => {
      expect(container.querySelector('#certResult')).toHaveTextContent('Generated! SFDI: 167261211635')
    })
    expect(post).toHaveBeenCalledWith('/api/certs/device', { deviceType: 1, hwSerialNum: 'PW-INV-001' })
  })

  it('reports a device cert failure rather than leaving the result blank', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: false, error: 'no CA loaded', status: 503 })

    const { container } = render(CertPanel)
    await fireEvent.click(screen.getByRole('button', { name: 'Generate Device Cert' }))

    await waitFor(() => {
      expect(container.querySelector('#certResult')).toHaveTextContent('Error: no CA loaded')
    })
  })

  it('splits the server cert hosts field into the hosts array the route expects', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { certPEM: 'PEM' } })

    const { container } = render(CertPanel)
    await fireEvent.input(container.querySelector('#serverCertHosts') as HTMLInputElement, {
      target: { value: 'localhost, 127.0.0.1 ,sep2.example' },
    })
    await fireEvent.click(screen.getByTestId('generate-server-cert'))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/certs/server', {
      hosts: ['localhost', '127.0.0.1', 'sep2.example'],
    })
  })

  it('refuses an empty server cert host list instead of posting an empty SAN set', async () => {
    const post = vi.spyOn(api, 'postJSON')

    render(CertPanel)
    await fireEvent.click(screen.getByTestId('generate-server-cert'))

    await waitFor(() => {
      expect(screen.getByTestId('server-cert-result')).toHaveTextContent('Enter at least one host or IP.')
    })
    expect(post).not.toHaveBeenCalled()
  })

  it('offers the CA as a direct download from the CA route', () => {
    render(CertPanel)

    const link = screen.getByTestId('download-ca')
    expect(link).toHaveAttribute('href', '/api/certs/ca')
  })
})
