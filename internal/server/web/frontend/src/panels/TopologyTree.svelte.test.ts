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
    expect(rendered.textContent).toContain('DEV edev 7 (SFDI=167261211635, ON)')
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

    await waitFor(() => expect(fetchSpy).toHaveBeenCalledTimes(1))
    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit]
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
})
