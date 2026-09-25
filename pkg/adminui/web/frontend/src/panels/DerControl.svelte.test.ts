// This panel is a deliberate stub: the admin DER control route does not
// exist on the server. The load-bearing assertion is the REFUSAL, that
// clicking Send issues no request at all, so a later change that quietly
// wires this form to an invented endpoint fails here.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import DerControl from './DerControl.svelte'
import * as api from '../lib/api'

describe('DerControl', () => {
  it('sends no request when submitted, and says so on the page', async () => {
    const post = vi.spyOn(api, 'postJSON')
    const get = vi.spyOn(api, 'fetchJSON')
    const fetchSpy = vi.spyOn(globalThis, 'fetch')

    const { container } = render(DerControl)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => {
      expect(container.querySelector('#controlResult')).toHaveTextContent('Control "connect" sent')
    })
    expect(post).not.toHaveBeenCalled()
    expect(get).not.toHaveBeenCalled()
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(screen.getByTestId('der-control-stub-note')).toHaveTextContent('POST /api/der/controls')
  })

  it('echoes the selected control type and the typed value', async () => {
    const { container } = render(DerControl)

    await fireEvent.change(container.querySelector('#controlType') as HTMLSelectElement, {
      target: { value: 'maxlim' },
    })
    await fireEvent.input(container.querySelector('#controlValue') as HTMLInputElement, {
      target: { value: '4000' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => {
      expect(container.querySelector('#controlResult')).toHaveTextContent(
        'Control "maxlim" sent (value: 4000)',
      )
    })
  })
})
