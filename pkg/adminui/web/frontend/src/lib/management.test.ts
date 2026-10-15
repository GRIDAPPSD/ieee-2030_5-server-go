import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from './api'
import { listPairs, pairsKey, removePair } from './management'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('listPairs', () => {
  it('asks once per device by manager and returns uppercase pairs', async () => {
    const get = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
      const manager = new URL(path, 'http://x').searchParams.get('manager')
      const managed = manager === 'AA' ? ['bb', 'CC'] : []
      return { ok: true, data: { managerLFDI: manager, managedLFDIs: managed } } as never
    })

    const res = await listPairs(['aa', 'DD'])

    expect(get.mock.calls.map((c) => c[0])).toEqual([
      '/api/management-pairs?manager=AA',
      '/api/management-pairs?manager=DD',
    ])
    expect(res).toEqual({
      ok: true,
      data: [
        { managerLFDI: 'AA', managedLFDI: 'BB' },
        { managerLFDI: 'AA', managedLFDI: 'CC' },
      ],
    })
  })

  it('reads a null managedLFDIs as no pairs', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValue({ ok: true, data: { managerLFDI: 'AA', managedLFDIs: null } } as never)

    expect(await listPairs(['AA'])).toEqual({ ok: true, data: [] })
  })

  it('fails the whole list when any device lookup fails', async () => {
    vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({ ok: true, data: { managerLFDI: 'AA', managedLFDIs: [] } } as never)
    vi.spyOn(api, 'fetchJSON').mockResolvedValueOnce({ ok: false, error: 'boom', status: 500 })

    expect(await listPairs(['AA', 'BB'])).toEqual({ ok: false, error: 'boom', status: 500 })
  })
})

describe('removePair', () => {
  it('deletes by the managed LFDI', async () => {
    const del = vi.spyOn(api, 'deleteJSON').mockResolvedValue({ ok: true, data: undefined as never })

    await removePair('ab12')

    expect(del).toHaveBeenCalledWith('/api/management-pairs?managed=AB12')
  })
})

describe('pairsKey', () => {
  it('is the same for the same LFDIs in any order and case', () => {
    expect(pairsKey(['bb', 'AA'])).toBe(pairsKey(['aa', 'BB']))
    expect(pairsKey(['AA'])).not.toBe(pairsKey(['AA', 'BB']))
  })
})
