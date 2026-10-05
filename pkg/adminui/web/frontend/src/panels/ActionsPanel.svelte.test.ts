import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import ActionsPanel from './ActionsPanel.svelte'
import * as api from '../lib/api'

afterEach(() => {
  vi.restoreAllMocks()
})

const schema = {
  actions: [
    { id: 'go', label: 'Go', fields: [{ name: 'n', label: 'N', kind: 'integer', min: 1, max: 3 }] },
    { id: 'pub', label: 'Pub', fields: [{ name: 'on', label: 'On', kind: 'toggle' }] },
  ],
}

describe('ActionsPanel', () => {
  it('reads the schema for the panel and renders one form per action', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: schema })
    const { unmount } = render(ActionsPanel, { props: { id: 'bridge' } })
    expect(await screen.findByTestId('action-go')).toBeInTheDocument()
    expect(screen.getByTestId('action-pub')).toBeInTheDocument()
    expect(spy.mock.calls[0][0]).toBe('/api/ui/panels/bridge/actions')
    unmount()
  })

  it('aborts the schema read on unmount', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockReturnValue(new Promise(() => {}))
    const { unmount } = render(ActionsPanel, { props: { id: 'bridge' } })
    const signal = spy.mock.calls[0][1]?.signal
    expect(signal?.aborted).toBe(false)
    unmount()
    expect(signal?.aborted).toBe(true)
  })

  it('says why the schema could not load, and Reload reads it again', async () => {
    const spy = vi
      .spyOn(api, 'fetchJSON')
      .mockResolvedValueOnce({ ok: false, status: 429, error: 'too many panel actions' })
      .mockResolvedValueOnce({ ok: true, data: schema })
    const { unmount } = render(ActionsPanel, { props: { id: 'bridge' } })
    expect(await screen.findByTestId('actions-error')).toHaveTextContent('Too many requests')
    await fireEvent.click(screen.getByRole('button', { name: 'Reload' }))
    expect(await screen.findByTestId('action-go')).toBeInTheDocument()
    expect(spy).toHaveBeenCalledTimes(2)
    expect(screen.queryByTestId('actions-error')).toBeNull()
    unmount()
  })

  it('refuses a schema it cannot read instead of rendering part of it', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: { actions: [{ id: 'x', label: 'X', fields: [{ name: 'n', label: 'N', kind: 'integer' }] }] } })
    const { unmount } = render(ActionsPanel, { props: { id: 'bridge' } })
    expect(await screen.findByTestId('actions-error')).toHaveTextContent('could not be read')
    expect(screen.queryByTestId('action-x')).toBeNull()
    unmount()
  })
})
