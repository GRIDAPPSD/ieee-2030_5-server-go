// The picker half of PanelView: what it sends, what it stores, and what it
// keeps on screen when the server refuses. fetchJSON is mocked by path and
// the clock is faked; every test unmounts what it mounts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/svelte'
import PanelView from './PanelView.svelte'
import * as api from '../lib/api'
import { PANEL_POLL_MS } from '../lib/descriptor'
import type { Descriptor, PickerChoice } from '../lib/descriptor'

const descriptor = (heading: string): Descriptor => ({
  version: 2,
  sections: [{ kind: 'table', heading, prose: [], empty: '', body: { columns: ['A'], rows: [] } }],
})

const choices = (n: number): PickerChoice[] => Array.from({ length: n }, (_, i) => ({ id: `id${i}`, label: `Battery ${i}` }))

const KEY = 'adminui.picker.graph'

type Handler = (path: string) => Awaited<ReturnType<typeof api.fetchJSON>>

// Routes /choices to the choices body and everything else to view().
function mockApi(n: number, view: Handler) {
  return vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path.includes('/choices')) return { ok: true, data: { max: 16, choices: choices(n) } } as never
    return view(path) as never
  })
}

const viewPaths = (spy: ReturnType<typeof mockApi>) =>
  spy.mock.calls.map((c) => c[0] as string).filter((p) => !p.includes('/choices'))

beforeEach(() => {
  vi.useFakeTimers()
  localStorage.clear()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  localStorage.clear()
})

describe('PanelView with a picker', () => {
  it('sends no query when nothing is selected, and shows the default view', async () => {
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Default') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph'])
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('Default')
    expect(screen.getByTestId('picker')).toBeInTheDocument()
    unmount()
  })

  it('carries a stored selection on the first request as repeated sel keys', async () => {
    localStorage.setItem(KEY, JSON.stringify(['id2', 'id0']))
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Sel') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph?sel=id2&sel=id0'])
    expect((screen.getByRole('checkbox', { name: 'Battery 2' }) as HTMLInputElement).checked).toBe(true)
    unmount()
  })

  it('sends the selection again on every poll', async () => {
    localStorage.setItem(KEY, JSON.stringify(['id1']))
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Sel') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS * 2)

    expect(viewPaths(spy)).toEqual(Array(3).fill('/api/ui/panels/graph?sel=id1'))
    unmount()
  })

  it('discards corrupt stored JSON and shows the default view', async () => {
    localStorage.setItem(KEY, '{not json')
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Default') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph'])
    expect(screen.queryByRole('alert')).toBeNull()
    unmount()
  })

  it('never sends a stored id that has left the choices, and shows a count not the id', async () => {
    localStorage.setItem(KEY, JSON.stringify(['id1', 'departed-device', 'id3']))
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Sel') }))
    const { container, unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph?sel=id1&sel=id3'])
    expect(screen.getByTestId('picker-departed')).toHaveTextContent('1 saved choice is no longer available.')
    expect(container.textContent).not.toContain('departed-device')
    unmount()
  })

  it('sends no query when every stored id has left the choices', async () => {
    localStorage.setItem(KEY, JSON.stringify(['gone1', 'gone2']))
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Default') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph'])
    unmount()
  })

  it('never sends more than 16 ids, whatever storage holds', async () => {
    localStorage.setItem(KEY, JSON.stringify(choices(30).map((c) => c.id)))
    const spy = mockApi(40, () => ({ ok: true, data: descriptor('Sel') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    const sent = new URL(viewPaths(spy)[0], 'http://x').searchParams.getAll('sel')
    expect(sent).toHaveLength(16)
    expect(sent[15]).toBe('id15')
    unmount()
  })

  it('works when storage throws on read and on write', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota')
    })
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Default') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)
    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph'])

    await fireEvent.click(screen.getByRole('checkbox', { name: 'Battery 4' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy).at(-1)).toBe('/api/ui/panels/graph?sel=id4')
    expect(screen.queryByRole('alert')).toBeNull()
    unmount()
  })

  it('Apply stores the selection locally, aborts the pending poll and sends exactly one request', async () => {
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('Sel') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)
    const firstView = spy.mock.calls.find((c) => !(c[0] as string).includes('/choices'))
    const firstSignal = firstView?.[1]?.signal
    expect(firstSignal?.aborted).toBe(false)
    const before = viewPaths(spy).length

    await fireEvent.click(screen.getByRole('checkbox', { name: 'Battery 3' }))
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Battery 1' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await vi.advanceTimersByTimeAsync(0)

    expect(firstSignal?.aborted).toBe(true)
    expect(viewPaths(spy).slice(before)).toEqual(['/api/ui/panels/graph?sel=id3&sel=id1'])
    expect(JSON.parse(localStorage.getItem(KEY) ?? 'null')).toEqual(['id3', 'id1'])
    unmount()
  })

  it('shows a 400 in words and keeps the last good chart', async () => {
    let refuse = false
    const spy = mockApi(5, () =>
      refuse ? { ok: false, status: 400, error: 'invalid selection' } : { ok: true, data: descriptor('Good chart') },
    )
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('Good chart')

    refuse = true
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS)

    expect(spy).toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('The server refused this selection. The last view is still shown.')
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('Good chart')
    unmount()
  })

  it('retries once after one second, silently, when the read after Apply gets a 504', async () => {
    let calls = 0
    const spy = mockApi(5, () => {
      calls++
      return calls === 2 ? { ok: false, status: 504, error: 'x' } : { ok: true, data: descriptor('Ok') }
    })
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Battery 2' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await vi.advanceTimersByTimeAsync(0)

    expect(calls).toBe(2)
    expect(screen.queryByRole('alert')).toBeNull()
    await vi.advanceTimersByTimeAsync(999)
    expect(calls).toBe(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(calls).toBe(3)
    expect(viewPaths(spy).at(-1)).toBe('/api/ui/panels/graph?sel=id2')
    expect(screen.queryByRole('alert')).toBeNull()
    unmount()
  })

  it('shows the timeout message when the retry after Apply also gets a 504', async () => {
    let calls = 0
    mockApi(5, () => {
      calls++
      return calls === 1 ? { ok: true, data: descriptor('Ok') } : { ok: false, status: 504, error: 'x' }
    })
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)
    await fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await vi.advanceTimersByTimeAsync(1000)

    expect(screen.getByRole('alert')).toHaveTextContent('This panel did not answer in time. It will be retried.')
    unmount()
  })

  it('does not retry silently a 504 that did not follow an Apply', async () => {
    const spy = mockApi(5, () => ({ ok: false, status: 504, error: 'x' }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.getByRole('alert')).toBeInTheDocument()
    await vi.advanceTimersByTimeAsync(1000)
    expect(viewPaths(spy)).toHaveLength(1)
    unmount()
  })
})

describe('PanelView picker, round 2', () => {
  it('keeps refreshing the view with the last good selection when /choices fails, and says so in words', async () => {
    localStorage.setItem(KEY, JSON.stringify(['id1', 'id2']))
    let choicesOk = true
    const spy = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      if (path.includes('/choices')) {
        return (choicesOk ? { ok: true, data: { max: 16, choices: choices(5) } } : { ok: false, status: 500, error: 'boom' }) as never
      }
      return { ok: true, data: descriptor('View') } as never
    })
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.queryByTestId('choices-error')).toBeNull()

    choicesOk = false
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph?sel=id1&sel=id2', '/api/ui/panels/graph?sel=id1&sel=id2'])
    expect(screen.getByTestId('choices-error')).toHaveTextContent('Could not load the choices: boom')
    expect(screen.getByTestId('descriptor-heading')).toHaveTextContent('View')

    choicesOk = true
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS)
    expect(screen.queryByTestId('choices-error')).toBeNull()
    unmount()
  })

  it('refreshes the view with no query when /choices has never loaded', async () => {
    localStorage.setItem(KEY, JSON.stringify(['id1']))
    const spy = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      if (path.includes('/choices')) return { ok: false, status: 500, error: 'boom' } as never
      return { ok: true, data: descriptor('Default') } as never
    })
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph'])
    expect(screen.getByTestId('choices-error')).toBeInTheDocument()
    unmount()
  })

  it('clears its poll timer on unmount: no read of either kind follows', async () => {
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('x') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)
    const before = spy.mock.calls.length

    unmount()
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS * 3)
    expect(spy.mock.calls.length).toBe(before)
  })

  it('reads a stored selection with a repeated id once', async () => {
    localStorage.setItem(KEY, JSON.stringify(['id1', 'id1', 'id2', 'id1']))
    const spy = mockApi(5, () => ({ ok: true, data: descriptor('x') }))
    const { unmount } = render(PanelView, { props: { id: 'graph', picker: { max: 16 } } })
    await vi.advanceTimersByTimeAsync(0)

    expect(viewPaths(spy)).toEqual(['/api/ui/panels/graph?sel=id1&sel=id2'])
    unmount()
  })
})

describe('PanelView without a picker', () => {
  it('never reads choices, shows no control and sends no query, even with a value stored under its id', async () => {
    localStorage.setItem('adminui.picker.plain', JSON.stringify(['id1']))
    const spy = vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: descriptor('Plain') })
    const { unmount } = render(PanelView, { props: { id: 'plain' } })
    await vi.advanceTimersByTimeAsync(PANEL_POLL_MS)

    expect(spy.mock.calls.map((c) => c[0])).toEqual(['/api/ui/panels/plain', '/api/ui/panels/plain'])
    expect(screen.queryByTestId('picker')).toBeNull()
    unmount()
  })
})
