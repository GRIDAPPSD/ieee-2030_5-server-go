// mRID is the FSA's identity on the wire, so the table's rendered mRID,
// description and primacy are asserted against the supplied values, and
// the unassign request is asserted to carry that same mRID rather than a
// row index.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import FsaCatalog from './FsaCatalog.svelte'
import * as api from '../lib/api'
import type { AdminFSA } from '../lib/fsa'

const fsas: AdminFSA[] = [
  {
    href: '/api/fsas/fsa-a',
    mRID: 'fsa-a',
    description: 'roof fleet',
    primacy: 3,
    programs: ['/derp/1'],
    devices: ['7'],
  },
  { href: '/api/fsas/fsa-b', mRID: 'fsa-b', description: 'curtailment', primacy: 0 },
]

describe('FsaCatalog', () => {
  it('renders each FSA mRID, description and primacy as supplied', () => {
    render(FsaCatalog, { props: { fsas, onChanged: () => {} } })

    const mrids = screen.getAllByTestId('fsa-catalog-mrid')
    expect(mrids[0]).toHaveTextContent('fsa-a')
    expect(mrids[1]).toHaveTextContent('fsa-b')

    const descriptions = screen.getAllByTestId('fsa-catalog-description')
    expect(descriptions[0]).toHaveTextContent('roof fleet')
    expect(descriptions[1]).toHaveTextContent('curtailment')

    const primacies = screen.getAllByTestId('fsa-catalog-primacy')
    expect(primacies[0]).toHaveTextContent('3')
    expect(primacies[1]).toHaveTextContent('0')
  })

  it('unassigns a device with the FSA href built from that FSA mRID', async () => {
    const del = vi.spyOn(api, 'deleteJSON').mockResolvedValue({ ok: true, data: undefined })
    const onChanged = vi.fn()

    render(FsaCatalog, { props: { fsas, onChanged } })
    await fireEvent.click(screen.getByTestId('unassign-fsa-a-7'))

    await waitFor(() => expect(del).toHaveBeenCalledTimes(1))
    expect(del).toHaveBeenCalledWith('/api/devices/7/fsa-assignment?fsaHref=%2Fapi%2Ffsas%2Ffsa-a')
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it('surfaces an unassign failure instead of dropping it', async () => {
    vi.spyOn(api, 'deleteJSON').mockResolvedValue({
      ok: false,
      error: 'assignment not found',
      status: 404,
    })

    render(FsaCatalog, { props: { fsas, onChanged: () => {} } })
    await fireEvent.click(screen.getByTestId('unassign-fsa-a-7'))

    const err = await screen.findByTestId('fsa-catalog-error')
    expect(err).toHaveTextContent('Unassign failed (404): assignment not found')
  })

  it('renders an empty state rather than an empty table when no templates exist', () => {
    render(FsaCatalog, { props: { fsas: [], onChanged: () => {} } })

    expect(screen.getByTestId('fsa-catalog-empty')).toHaveTextContent('No FSA templates created yet.')
    expect(screen.queryByTestId('fsa-catalog-table')).toBeNull()
  })
})
