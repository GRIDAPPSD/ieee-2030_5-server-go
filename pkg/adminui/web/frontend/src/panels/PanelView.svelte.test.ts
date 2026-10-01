// PanelView owns the poll timer and the in-flight read. The tests drive a
// mocked fetchJSON under fake timers and unmount what they mount.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import PanelView from './PanelView.svelte'
import * as api from '../lib/api'
import { PANEL_POLL_MS } from '../lib/descriptor'
import type { Descriptor } from '../lib/descriptor'

const descriptor = (heading: string): Descriptor => ({
  version: 2,
  sections: [{ kind: 'table', heading, prose: [], empty: '', body: { columns: ['A'], rows: [] } }],
})

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('PanelView', () => {
  it('reads the panel by id, renders its descriptor, and reads again every poll interval', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: descriptor('First') })
    const { unmount } = render(PanelView, { props: { id: 'gridappsd-registry' } })

    await vi.advanceTimersByTimeAsync(0)
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0]).toBe('/api/ui/panels/gridappsd-registry')
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('First')

    spy.mockResolvedValue({ ok: true, data: descriptor('Second') })
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS - 1)
    expect(spy).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(spy).toHaveBeenCalledTimes(2)
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('Second')
    unmount()
  })

  it('stops polling and aborts the in-flight read when unmounted', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: descriptor('x') })
    const { unmount } = render(PanelView, { props: { id: 'p' } })
    await vi.advanceTimersByTimeAsync(0)
    const signal = spy.mock.calls[0][1]?.signal
    expect(signal?.aborted).toBe(false)

    unmount()
    expect(signal?.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS * 4)
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('re-reads under the new id when the id changes, and stops polling the old one', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: descriptor('x') })
    const { rerender, unmount } = render(PanelView, { props: { id: 'a' } })
    await vi.advanceTimersByTimeAsync(0)
    await rerender({ id: 'b' })
    await vi.advanceTimersByTimeAsync(0)
    expect(spy.mock.calls.map((c) => c[0])).toEqual(['/api/ui/panels/a', '/api/ui/panels/b'])

    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS)
    expect(spy.mock.calls.map((c) => c[0])).toEqual(['/api/ui/panels/a', '/api/ui/panels/b', '/api/ui/panels/b'])
    unmount()
  })

  it.each([
    [504, 'The server said so', 'This panel did not answer in time. It will be retried.'],
    [503, 'The server said so', 'The request for this panel was canceled. It will be retried.'],
    [401, 'admin authentication required', 'Admin session required. Sign in again.'],
    [404, 'no such panel', 'No such panel.'],
    [500, 'panel failed', 'Could not load this panel: panel failed'],
    [0, 'network error', 'Could not load this panel: network error'],
  ])('shows status %i as words in an alert, never as an empty panel', async (status, error, words) => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, status, error })
    const { unmount } = render(PanelView, { props: { id: 'p' } })
    await vi.advanceTimersByTimeAsync(0)

    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent(words)
    expect(screen.queryByTestId('descriptor-section')).toBeNull()
    expect(screen.queryByTestId('descriptor-empty')).toBeNull()
    unmount()
  })

  it('keeps polling after a failed read and recovers when the panel answers', async () => {
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: false, status: 504, error: 'x' })
    const { unmount } = render(PanelView, { props: { id: 'p' } })
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByRole('alert')).toBeInTheDocument()

    spy.mockResolvedValue({ ok: true, data: descriptor('Back') })
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS)
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('Back')
    unmount()
  })
})
