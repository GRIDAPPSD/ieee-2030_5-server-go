import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from './api'
import { resetDeviceProfiles } from './devices'
import { seedDeviceProfiles } from './devices.fixture'
import { applyPairs, listPairs, pairFailures, pairsKey, pickableDevices, removePair } from './management'

afterEach(() => {
  vi.restoreAllMocks()
  resetDeviceProfiles()
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

describe('pickableDevices', () => {
  const devs = ['AA', 'BB', 'CC', 'DD', 'EE'].map((lfdi) => ({ lfdi }))

  it('drops the manager, stored aggregators, managers in pairs, and devices held elsewhere', async () => {
    await seedDeviceProfiles([{ lfdi: 'BB', name: '', kind: 'aggregator' }])
    const pairs = [
      { managerLFDI: 'CC', managedLFDI: 'DD' },
      { managerLFDI: 'FF', managedLFDI: 'EE' },
    ]

    expect(pickableDevices(devs, pairs).map((d) => d.lfdi)).toEqual(['AA'])
    expect(pickableDevices(devs, [], 'aa').map((d) => d.lfdi)).toEqual(['CC', 'DD', 'EE'])
    expect(pickableDevices(devs, pairs, 'aa')).toEqual([])
  })

  it('keeps a device this manager already holds, so an edit can untick it', () => {
    const pairs = [{ managerLFDI: 'AA', managedLFDI: 'BB' }]

    expect(pickableDevices(devs, pairs, 'AA').map((d) => d.lfdi)).toEqual(['BB', 'CC', 'DD', 'EE'])
  })
})

describe('applyPairs', () => {
  it('removes then adds, and one refused pair does not stop the rest', async () => {
    const calls: string[] = []
    vi.spyOn(api, 'deleteJSON').mockImplementation(async (path: string) => {
      calls.push('del ' + path)
      return { ok: true, data: undefined as never }
    })
    vi.spyOn(api, 'postJSON').mockImplementation(async (_p: string, body: unknown) => {
      const b = body as { managedLFDI: string }
      calls.push('add ' + b.managedLFDI)
      return b.managedLFDI === 'BB'
        ? { ok: false, error: 'device already has a different manager', status: 409 }
        : ({ ok: true, data: body } as never)
    })

    const out = await applyPairs('aa', ['bb', 'cc'], ['dd'])

    expect(calls).toEqual(['del /api/management-pairs?managed=DD', 'add BB', 'add CC'])
    expect(out.map((o) => [o.lfdi, o.action, o.result.ok])).toEqual([
      ['dd', 'remove', true],
      ['bb', 'add', false],
      ['cc', 'add', true],
    ])
    expect(pairFailures(out, (l) => `dev ${l}`)).toEqual(['dev bb: device already has a different manager (409)'])
  })
})
