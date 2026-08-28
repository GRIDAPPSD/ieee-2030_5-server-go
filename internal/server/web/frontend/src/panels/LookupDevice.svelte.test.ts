import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import LookupDevice from './LookupDevice.svelte'
import * as api from '../lib/api'

const LFDI = '0123456789ABCDEF0123456789ABCDEF01234567'

describe('LookupDevice', () => {
  it('renders the found device href, SFDI and enabled state from the response', async () => {
    const get = vi.spyOn(api, 'fetchJSON').mockResolvedValue({
      ok: true,
      data: { found: true, device: { sfdi: '167261211635', lfdi: LFDI, href: '/edev/2', enabled: true } },
    })

    const { container } = render(LookupDevice)
    await fireEvent.input(container.querySelector('#lookupLFDI') as HTMLInputElement, {
      target: { value: LFDI },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Lookup' }))

    await waitFor(() => {
      expect(container.querySelector('#lookupResult')).toHaveTextContent(
        'Found: /edev/2 SFDI=167261211635 enabled=true',
      )
    })
    expect(get).toHaveBeenCalledWith(`/api/devices/by-lfdi/${LFDI}`)
  })

  it('reports an unregistered LFDI as not found, not as an error', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: { found: false } })

    const { container } = render(LookupDevice)
    await fireEvent.input(container.querySelector('#lookupLFDI') as HTMLInputElement, {
      target: { value: LFDI },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Lookup' }))

    await waitFor(() => {
      expect(container.querySelector('#lookupResult')).toHaveTextContent(
        'No device registered for that LFDI.',
      )
    })
  })

  it('does not issue a lookup for an empty LFDI', async () => {
    const get = vi.spyOn(api, 'fetchJSON')

    const { container } = render(LookupDevice)
    await fireEvent.click(screen.getByRole('button', { name: 'Lookup' }))

    await waitFor(() => {
      expect(container.querySelector('#lookupResult')).toHaveTextContent('Enter an LFDI to look up.')
    })
    expect(get).not.toHaveBeenCalled()
  })
})
