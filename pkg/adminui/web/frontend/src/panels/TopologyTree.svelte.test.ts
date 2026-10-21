// The tree is the only place the SY -> FD -> SP -> DEV hierarchy and the
// FSAs attached at each level are shown together, so the assertions cover
// the rendered identity values (device SFDI, FSA mRID, program href) and
// the per-mRID control ids the Playwright suite addresses.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import TopologyTree from './TopologyTree.svelte'
import * as api from '../lib/api'
import type { TopologyNode } from '../lib/fsa'

const tree: TopologyNode = {
  kind: 'SY',
  id: 'sy',
  label: 'System',
  // id and mRID deliberately differ: every request keyed off this FSA must
  // use the mRID, and a test where the two match cannot tell them apart.
  fsas: [{ id: 'internal-7', mRID: 'fsa-a', description: 'roof fleet', programs: ['/derp/1'] }],
  children: [
    {
      kind: 'FD',
      id: 'fd',
      label: 'Feeder',
      children: [
        {
          kind: 'SP',
          id: 'sp',
          label: 'ServicePoint',
          children: [
            {
              kind: 'DEV',
              id: '7',
              label: 'edev 7',
              sfdi: '167261211635',
              lfdi: '3E4F45AB31EDFE5B67E343E5E4562E31984E23E5',
              enabled: true,
            },
          ],
        },
      ],
    },
  ],
}

describe('TopologyTree', () => {
  it('renders every level of the hierarchy with the supplied device SFDI and enabled state', () => {
    const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })

    const rendered = container.querySelector('#topologyTree') as HTMLElement
    expect(rendered).not.toBeNull()
    expect(rendered.textContent).toContain('SY System')
    expect(rendered.textContent).toContain('FD Feeder')
    expect(rendered.textContent).toContain('SP ServicePoint')
    expect(rendered.textContent).toContain('DEV edev 7 (167261211635, ON)')
  })

  it('renders the FSA mRID, description and attached program href', () => {
    const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })

    const rendered = container.querySelector('#topologyTree') as HTMLElement
    expect(rendered.textContent).toContain('FSA fsa-a - roof fleet')
    expect(rendered.textContent).toContain('PROG /derp/1')
  })

  it('keeps the per-mRID attach input and result ids, and offers Delete only at the system root', () => {
    const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })

    expect(container.querySelector('#attachInp-fsa-a')).not.toBeNull()
    expect(container.querySelector('#attachResult-fsa-a')).not.toBeNull()
    expect(screen.getAllByRole('button', { name: 'Delete' })).toHaveLength(1)
  })

  it('attaches a program by the FSA mRID and reports the attached href', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
      ok: true,
      data: { programHref: '/derp/2' },
    })
    const onRefresh = vi.fn()

    const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh } })
    await fireEvent.input(container.querySelector('#attachInp-fsa-a') as HTMLInputElement, {
      target: { value: '/derp/2' },
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Attach program' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/fsas/fsa-a/programs', { programHref: '/derp/2' })
    expect(container.querySelector('#attachResult-fsa-a')).toHaveTextContent('Attached /derp/2')
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('detaches a program by its href on the owning FSA', async () => {
    const del = vi.spyOn(api, 'deleteJSON').mockResolvedValue({ ok: true, data: undefined })

    render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })
    await fireEvent.click(screen.getByTestId('detach-program-fsa-a'))

    await waitFor(() => expect(del).toHaveBeenCalledTimes(1))
    expect(del).toHaveBeenCalledWith('/api/fsas/fsa-a/programs?href=%2Fderp%2F1')
  })

  it('deletes the FSA template with a DELETE to its own mRID, and refreshes', async () => {
    // Asserted at the fetch boundary rather than on api.deleteJSON: the
    // METHOD is only visible here, and a destructive route sent with the
    // wrong verb or the wrong mRID is the failure this test exists for.
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 204,
      json: () => Promise.reject(new Error('204 carries no body')),
    } as unknown as Response)
    const onRefresh = vi.fn()

    render(TopologyTree, { props: { tree, error: '', onRefresh } })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

    // The tree also reads the program list for its picker, so that GET is
    // set aside and every other request must be the one DELETE.
    const others = () =>
      fetchSpy.mock.calls.filter((c) => !String(c[0]).startsWith('/api/der-programs')) as [string, RequestInit][]
    await waitFor(() => expect(others()).toHaveLength(1))
    const [url, init] = others()[0]
    expect(url).toBe('/api/fsas/fsa-a')
    expect(init.method).toBe('DELETE')
    expect(init.credentials).toBe('same-origin')
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('surfaces a failed delete and does NOT report a refresh', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: () => Promise.resolve({ error: 'fsa still assigned to a device' }),
    } as unknown as Response)
    const onRefresh = vi.fn()

    const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh } })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

    await waitFor(() => {
      expect(container.querySelector('#attachResult-fsa-a')).toHaveTextContent(
        'Delete failed (409): fsa still assigned to a device',
      )
    })
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it('collapses and re-expands a branch without refetching the topology', async () => {
    const onRefresh = vi.fn()
    const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh } })

    await fireEvent.click(screen.getByTestId('topology-node-SY-sy'))
    await waitFor(() => {
      expect((container.querySelector('#topologyTree') as HTMLElement).textContent).not.toContain('FD Feeder')
    })
    expect(onRefresh).not.toHaveBeenCalled()

    await fireEvent.click(screen.getByTestId('topology-node-SY-sy'))
    await waitFor(() => {
      expect((container.querySelector('#topologyTree') as HTMLElement).textContent).toContain('FD Feeder')
    })
  })

  it('reports a topology load failure in place of the tree', () => {
    render(TopologyTree, { props: { tree: null, error: 'request failed with status 500', onRefresh: () => {} } })

    expect(screen.getByTestId('topology-error')).toHaveTextContent(
      'Error loading topology: request failed with status 500',
    )
  })

  describe('program picker', () => {
    const item = (over: Record<string, unknown>) => ({
      device: '7', fsa: 'fsa-a', id: 'derp-1', href: '/edev/7/fsa/fsa-a/derp/derp-1', mRID: 'AB',
      description: 'roof fleet', primacy: 65, version: 2, origin: 'template', onRestart: 'kept', ...over,
    })

    function catalogReply(programs: unknown[]) {
      return vi.spyOn(globalThis, 'fetch').mockResolvedValue({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ persists: true, total: programs.length, start: 0, limit: 500, programs }),
      } as Response)
    }

    it('offers the programs GET /api/der-programs returns, by href, on the attach input', async () => {
      const get = catalogReply([
        item({}),
        item({ id: 'derp-2', href: '/edev/8/fsa/fsa-b/derp/derp-2', device: '8', fsa: 'fsa-b', description: 'night' }),
      ])
      const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })

      await waitFor(() => expect(container.querySelectorAll('datalist#attachPrograms option')).toHaveLength(2))
      const options = Array.from(container.querySelectorAll('datalist#attachPrograms option'))
      expect(options.map((o) => o.getAttribute('value'))).toEqual([
        '/edev/7/fsa/fsa-a/derp/derp-1',
        '/edev/8/fsa/fsa-b/derp/derp-2',
      ])
      expect(options[0].getAttribute('label')).toBe('roof fleet (device 7, fsa-a, template)')
      expect(get.mock.calls[0][0]).toBe('/api/der-programs?limit=500&start=0')
      const input = container.querySelector('#attachInp-fsa-a') as HTMLInputElement
      expect(input.getAttribute('list')).toBe('attachPrograms')
    })

    it('attaches the href picked from the list and keeps the result span', async () => {
      catalogReply([item({})])
      const post = vi.spyOn(api, 'postJSON').mockResolvedValue({
        ok: true,
        data: { programHref: '/edev/7/fsa/fsa-a/derp/derp-1' },
      })
      const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })
      await waitFor(() => expect(container.querySelectorAll('datalist#attachPrograms option')).toHaveLength(1))

      await fireEvent.input(container.querySelector('#attachInp-fsa-a') as HTMLInputElement, {
        target: { value: '/edev/7/fsa/fsa-a/derp/derp-1' },
      })
      await fireEvent.click(screen.getByRole('button', { name: 'Attach program' }))

      await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
      expect(post).toHaveBeenCalledWith('/api/fsas/fsa-a/programs', { programHref: '/edev/7/fsa/fsa-a/derp/derp-1' })
      expect(container.querySelector('#attachResult-fsa-a')).toHaveTextContent(
        'Attached /edev/7/fsa/fsa-a/derp/derp-1',
      )
    })

    function pending() {
      const reqs: { signal: AbortSignal; resolve: (programs: unknown[]) => void }[] = []
      vi.spyOn(globalThis, 'fetch').mockImplementation(
        (_url, init) =>
          new Promise((done) =>
            reqs.push({
              signal: init?.signal as AbortSignal,
              resolve: (programs) =>
                done({
                  ok: true,
                  status: 200,
                  json: () => Promise.resolve({ persists: true, total: programs.length, programs }),
                } as Response),
            }),
          ),
      )
      return reqs
    }

    it('reads the list again when the tree changes, aborts the old read, and ignores its late answer', async () => {
      const reqs = pending()
      const { container, rerender } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })
      await waitFor(() => expect(reqs).toHaveLength(1))

      await rerender({ tree: { ...tree }, error: '', onRefresh: () => {} })
      await waitFor(() => expect(reqs).toHaveLength(2))
      expect(reqs[0].signal.aborted).toBe(true)
      expect(reqs[1].signal.aborted).toBe(false)

      reqs[1].resolve([item({ id: 'new', href: '/edev/7/fsa/fsa-a/derp/new' })])
      await waitFor(() => expect(container.querySelectorAll('datalist#attachPrograms option')).toHaveLength(1))
      reqs[0].resolve([item({ id: 'old', href: '/edev/7/fsa/fsa-a/derp/old' })])
      await new Promise((r) => setTimeout(r, 30))

      const values = Array.from(container.querySelectorAll('datalist#attachPrograms option')).map((o) =>
        o.getAttribute('value'),
      )
      expect(values).toEqual(['/edev/7/fsa/fsa-a/derp/new'])
      expect(screen.queryByTestId('program-picker-error')).toBeNull()
    })

    it('aborts the program read when the tree unmounts', async () => {
      const reqs = pending()
      const { unmount } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })
      await waitFor(() => expect(reqs).toHaveLength(1))
      expect(reqs[0].signal.aborted).toBe(false)

      unmount()

      expect(reqs[0].signal.aborted).toBe(true)
    })

    it('says so when the program list cannot be read, and still accepts a typed href', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue({
        ok: false,
        status: 500,
        json: () => Promise.resolve({ error: 'program list failed' }),
      } as Response)
      const { container } = render(TopologyTree, { props: { tree, error: '', onRefresh: () => {} } })

      expect(await screen.findByTestId('program-picker-error')).toHaveTextContent(
        'Could not load the program list (500): program list failed',
      )
      expect(container.querySelector('#attachInp-fsa-a')).not.toBeNull()
      expect(container.querySelectorAll('datalist#attachPrograms option')).toHaveLength(0)
    })
  })
})
