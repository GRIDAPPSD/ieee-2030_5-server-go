// Issue 561 criteria 8, 9, 10 and 15 at the shell level: what a tab switch
// does to the stream, the retained history and the shared FSA and topology
// state, and which tab sees the result of a mutation made on another. The
// fake admin API below is stateful, so a created FSA or an added device is
// served by the next GET exactly as the real endpoints would.
import { describe, expect, it, vi, type Mock } from 'vitest'
import { tick } from 'svelte'
import * as echarts from 'echarts/core'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import AdminShell from './AdminShell.svelte'
import * as api from './lib/api'
import * as dash from './lib/dashboard'
import * as router from './lib/router'
import type { DashboardData } from './lib/dashboard'
import type { AdminFSA, TopologyNode } from './lib/fsa'
import { installCanvasStub } from './test-canvas-stub'

installCanvasStub()

function dashboardData(timestamp: string, devices: DashboardData['devices']): DashboardData {
  return {
    timestamp,
    deviceCount: devices?.length ?? 0,
    mupCount: 0,
    tlsMode: 'TLS_AES_256_GCM_SHA384',
    uptime: '1m0s',
    devices,
  }
}

const DEVICE_1 = {
  sfdi: '167261211635',
  lfdi: '3E4F45AB31EDFE5B67E343E5E4562E31984E23E5',
  href: '/edev/1',
  enabled: true,
}

interface Fake {
  gets: Record<string, number>
  fsas: AdminFSA[]
  devices: NonNullable<DashboardData['devices']>
  push: (frame: DashboardData) => void
  disconnect: Mock<() => void>
  connect: ReturnType<typeof vi.spyOn>
  holdFsas: ((call: number) => Promise<void> | undefined) | null
}

function topologyOf(fake: Fake): TopologyNode {
  return {
    kind: 'SY',
    id: 'sy',
    label: 'System',
    fsas: fake.fsas.map((f) => ({
      id: f.mRID,
      mRID: f.mRID,
      description: f.description,
      programs: f.programs,
    })),
    children: fake.devices.map((d) => ({
      kind: 'DEV',
      id: d.href.substring(d.href.lastIndexOf('/') + 1),
      label: d.href,
      sfdi: d.sfdi,
      lfdi: d.lfdi,
      enabled: d.enabled ?? undefined,
    })),
  }
}

function installFake(): Fake {
  const fake: Fake = {
    gets: {},
    fsas: [{ href: '/api/fsas/fsa-a', mRID: 'fsa-a', description: 'Feeder A', primacy: 1, programs: [], devices: [] }],
    devices: [{ ...DEVICE_1 }],
    push: () => {},
    disconnect: vi.fn<() => void>(),
    connect: undefined as never,
    holdFsas: null,
  }
  const countGet = (path: string) => {
    fake.gets[path] = (fake.gets[path] ?? 0) + 1
  }

  vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    countGet(path)
    if (path === '/dashboard/data') return { ok: true, data: dashboardData('09:00:00', fake.devices) } as never
    if (path === '/api/fsas') {
      // The snapshot is taken at call time, so a held call returns the
      // list as it was when the request was made.
      const snapshot = structuredClone(fake.fsas)
      await fake.holdFsas?.(fake.gets[path])
      return { ok: true, data: { fsas: snapshot } } as never
    }
    if (path === '/api/topology') return { ok: true, data: topologyOf(fake) } as never
    if (path === '/api/certs/device-types') {
      return { ok: true, data: { deviceTypes: [{ value: 1, name: 'generic', label: 'Generic' }] } } as never
    }
    if (path === '/api/derms/fleets') return { ok: true, data: [] } as never
    return { ok: true, data: {} } as never
  })

  vi.spyOn(api, 'postBody').mockImplementation(async (path: string) => {
    if (path === '/api/certs/info') {
      return { ok: true, data: { sfdi: '311635167262', lfdi: 'NEWLFDI000000000000000000000000000002', subject: 'CN=new' } } as never
    }
    return { ok: false, error: `unexpected ${path}`, status: 500 }
  })

  vi.spyOn(api, 'postJSON').mockImplementation(async (path: string, body: unknown) => {
    const payload = body as Record<string, unknown>
    if (path === '/api/devices') {
      const device = { sfdi: String(payload.sfdi), lfdi: String(payload.lfdi), href: '/edev/2', enabled: true }
      fake.devices.push(device)
      return { ok: true, data: device } as never
    }
    if (path === '/api/fsas') {
      const created: AdminFSA = {
        href: '/api/fsas/fsa-new',
        mRID: 'fsa-new',
        description: String(payload.description),
        primacy: 0,
        programs: [],
        devices: [],
      }
      fake.fsas.push(created)
      return { ok: true, data: created } as never
    }
    const assign = path.match(/^\/api\/devices\/([^/]+)\/fsa-assignment$/)
    if (assign) {
      const fsa = fake.fsas.find((f) => f.href === payload.fsaHref)
      fsa?.devices?.push(assign[1])
      return { ok: true, data: {} } as never
    }
    const attach = path.match(/^\/api\/fsas\/([^/]+)\/programs$/)
    if (attach) {
      fake.fsas.find((f) => f.mRID === attach[1])?.programs?.push(String(payload.programHref))
      return { ok: true, data: { programHref: payload.programHref } } as never
    }
    return { ok: false, error: `unexpected ${path}`, status: 500 }
  })

  vi.spyOn(api, 'deleteJSON').mockImplementation(async (path: string) => {
    const [pathname, query = ''] = path.split('?')
    const params = new URLSearchParams(query)
    const unassign = pathname.match(/^\/api\/devices\/([^/]+)\/fsa-assignment$/)
    if (unassign) {
      const fsa = fake.fsas.find((f) => f.href === params.get('fsaHref'))
      if (fsa) fsa.devices = (fsa.devices ?? []).filter((d) => d !== unassign[1])
      return { ok: true, data: undefined } as never
    }
    const detach = pathname.match(/^\/api\/fsas\/([^/]+)\/programs$/)
    if (detach) {
      const fsa = fake.fsas.find((f) => f.mRID === detach[1])
      if (fsa) fsa.programs = (fsa.programs ?? []).filter((p) => p !== params.get('href'))
      return { ok: true, data: undefined } as never
    }
    const del = pathname.match(/^\/api\/fsas\/([^/]+)$/)
    if (del) {
      fake.fsas = fake.fsas.filter((f) => f.mRID !== del[1])
      return { ok: true, data: undefined } as never
    }
    return { ok: false, error: `unexpected ${path}`, status: 500 }
  })

  fake.connect = vi.spyOn(dash, 'connectDashboard').mockImplementation((onData) => {
    fake.push = onData
    return fake.disconnect
  })
  return fake
}

async function renderAt(path: string, fake: Fake) {
  window.history.pushState({}, '', path)
  const result = render(AdminShell)
  await waitFor(() => expect(fake.connect).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(result.container.querySelector('.grid h2')).toBeTruthy())
  return result
}

async function goto(slug: string) {
  router.navigate(`/ui/${slug}`)
  await tick()
  await waitFor(() => expect(screen.getByTestId(`tab-${slug}`)).toHaveAttribute('aria-current', 'page'))
}

// Lets any in-flight refresh finish and any stray extra one show itself.
async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await tick()
}

const reads = (fake: Fake) => ({
  fsas: fake.gets['/api/fsas'] ?? 0,
  topology: fake.gets['/api/topology'] ?? 0,
})

describe('AdminShell stream across tab switches (criterion 8)', () => {
  it('opens the stream once and never closes it over six tab switches', async () => {
    const fake = installFake()
    await renderAt('/', fake)

    for (const slug of ['devices', 'fsas', 'control', 'certificates', 'derms', 'overview']) {
      await goto(slug)
    }
    await settle()

    expect(fake.connect).toHaveBeenCalledTimes(1)
    expect(fake.disconnect).not.toHaveBeenCalled()
  })
})

describe('AdminShell activity history across tab switches (criterion 9)', () => {
  it('draws every retained point on returning to Overview, including frames received while away', async () => {
    const fake = installFake()
    const { container } = await renderAt('/', fake)

    fake.push(dashboardData('09:00:05', fake.devices))
    fake.push(dashboardData('09:00:10', fake.devices))
    await tick()

    await goto('devices')
    fake.push(dashboardData('09:00:15', fake.devices))
    fake.push(dashboardData('09:00:20', fake.devices))
    fake.push(dashboardData('09:00:25', fake.devices))
    await tick()

    // The first authenticated read seeded one point; five frames followed.
    const retained = 6
    await goto('overview')

    const el = container.querySelector('#activityChart') as HTMLElement
    const chart = echarts.getInstanceByDom(el)
    expect(chart).toBeTruthy()
    // No frame arrives after the return: the series length read here is
    // what the chart drew from history alone.
    await waitFor(() => {
      const option = chart!.getOption() as { series: { data: unknown[] }[] }
      expect(option.series[0].data).toHaveLength(retained)
    })
    const option = chart!.getOption() as { xAxis: { data: string[] }[] }
    expect(option.xAxis[0].data).toEqual([
      '09:00:00',
      '09:00:05',
      '09:00:10',
      '09:00:15',
      '09:00:20',
      '09:00:25',
    ])
  })
})

describe('AdminShell reloads FSAs and topology on entering Devices or FSAs (criterion 15)', () => {
  it('issues one GET /api/fsas and one GET /api/topology per entry, and none for the other tabs', async () => {
    const fake = installFake()
    await renderAt('/', fake)
    await settle()
    expect(reads(fake)).toEqual({ fsas: 0, topology: 0 })

    for (const slug of ['control', 'certificates', 'derms', 'overview']) {
      await goto(slug)
      await settle()
    }
    expect(reads(fake)).toEqual({ fsas: 0, topology: 0 })

    await goto('devices')
    await waitFor(() => expect(reads(fake)).toEqual({ fsas: 1, topology: 1 }))
    await settle()
    expect(reads(fake)).toEqual({ fsas: 1, topology: 1 })

    await goto('fsas')
    await waitFor(() => expect(reads(fake)).toEqual({ fsas: 2, topology: 2 }))
    await settle()
    expect(reads(fake)).toEqual({ fsas: 2, topology: 2 })

    await goto('overview')
    await settle()
    expect(reads(fake)).toEqual({ fsas: 2, topology: 2 })

    await goto('devices')
    await waitFor(() => expect(reads(fake)).toEqual({ fsas: 3, topology: 3 }))
  })

  it('counts a hard load straight onto the Devices tab as one entry, not two', async () => {
    const fake = installFake()
    await renderAt('/ui/devices', fake)
    await waitFor(() => expect(reads(fake)).toEqual({ fsas: 1, topology: 1 }))
    await settle()
    expect(reads(fake)).toEqual({ fsas: 1, topology: 1 })
  })
})

describe('AdminShell overlapping reloads (criterion 15)', () => {
  it('keeps the newer FSA list when an older response lands after it', async () => {
    const fake = installFake()
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => (release = resolve))
    fake.holdFsas = (call) => (call === 1 ? gate : undefined)
    await renderAt('/ui/devices', fake)
    await waitFor(() => expect(fake.gets['/api/fsas']).toBe(1))

    fake.fsas.push({ href: '/api/fsas/fsa-new', mRID: 'fsa-new', description: 'Feeder New', primacy: 0, programs: [], devices: [] })
    await goto('fsas')
    const mrids = () => screen.queryAllByTestId('fsa-catalog-mrid').map((e) => e.textContent)
    await waitFor(() => expect(mrids()).toEqual(['fsa-a', 'fsa-new']))

    release()
    await settle()
    await settle()
    expect(mrids()).toEqual(['fsa-a', 'fsa-new'])
  })
})

describe('AdminShell mutations reach the other tab that shows the result (criterion 10)', () => {
  // Each test asserts the shell reloaded straight after the mutation, while
  // still on the tab it was made on: entering the other tab reloads too
  // (criterion 15), so a check made only after the switch could not tell a
  // mutation that refreshes from one that does not.
  async function reloadedAfter(fake: Fake, act: () => Promise<void>) {
    await settle()
    const before = reads(fake)
    await act()
    await waitFor(() => expect(reads(fake)).toEqual({ fsas: before.fsas + 1, topology: before.topology + 1 }))
    await settle()
    expect(reads(fake)).toEqual({ fsas: before.fsas + 1, topology: before.topology + 1 })
  }

  it('add device: the FSA Tree shows the new device', async () => {
    const fake = installFake()
    const { container } = await renderAt('/ui/devices', fake)

    await reloadedAfter(fake, async () => {
      await fireEvent.input(container.querySelector('#addDevCert') as HTMLTextAreaElement, {
        target: { value: '-----BEGIN CERTIFICATE-----\nMIIBnew\n-----END CERTIFICATE-----\n' },
      })
      await fireEvent.click(screen.getByRole('button', { name: 'Parse Cert' }))
      await waitFor(() => {
        expect((container.querySelector('#addDevSFDI') as HTMLInputElement).value).toBe('311635167262')
      })
      await fireEvent.input(container.querySelector('#addDevPIN') as HTMLInputElement, { target: { value: '1234' } })
      await fireEvent.click(screen.getByRole('button', { name: 'Add Device' }))
      await waitFor(() => expect(container.querySelector('#addDevResult')).toHaveTextContent('Created /edev/2'))
    })

    await goto('fsas')
    const node = await screen.findByTestId('topology-node-DEV-2')
    expect(node).toHaveTextContent('SFDI=311635167262, ON')
  })

  it('create FSA: the End Devices assign select offers the new FSA', async () => {
    const fake = installFake()
    const { container } = await renderAt('/ui/fsas', fake)

    await reloadedAfter(fake, async () => {
      await fireEvent.input(container.querySelector('#newFSADesc') as HTMLInputElement, {
        target: { value: 'Feeder New' },
      })
      await fireEvent.click(screen.getByRole('button', { name: 'Create FSA' }))
      await waitFor(() => expect(container.querySelector('#createFSAResult')).toHaveTextContent('mRID=fsa-new'))
    })

    await goto('devices')
    const options = Array.from(container.querySelectorAll('#assignSel-1 option')).map(
      (o) => (o as HTMLOptionElement).value,
    )
    expect(options).toEqual(['', '/api/fsas/fsa-a', '/api/fsas/fsa-new'])
  })

  it('assign: FSA Templates lists the device as assigned and the topology is reloaded', async () => {
    const fake = installFake()
    const { container } = await renderAt('/ui/devices', fake)

    await reloadedAfter(fake, async () => {
      await fireEvent.change(container.querySelector('#assignSel-1') as HTMLSelectElement, {
        target: { value: '/api/fsas/fsa-a' },
      })
      await fireEvent.click(screen.getByRole('button', { name: 'Assign' }))
    })
    expect(fake.fsas[0].devices).toEqual(['1'])

    await goto('fsas')
    expect(await screen.findByTestId('unassign-fsa-a-1')).toBeInTheDocument()
  })

  it('unassign: FSA Templates drops the device and the shell reloads the FSA list for End Devices', async () => {
    const fake = installFake()
    fake.fsas[0].devices = ['1']
    await renderAt('/ui/fsas', fake)
    const unassign = await screen.findByTestId('unassign-fsa-a-1')

    await reloadedAfter(fake, async () => {
      await fireEvent.click(unassign)
    })
    expect(fake.fsas[0].devices).toEqual([])
    expect(screen.queryByTestId('unassign-fsa-a-1')).toBeNull()
  })

  it('attach: the FSA Tree shows the attached program', async () => {
    const fake = installFake()
    const { container } = await renderAt('/ui/fsas', fake)

    await reloadedAfter(fake, async () => {
      await fireEvent.input(container.querySelector('#attachInp-fsa-a') as HTMLInputElement, {
        target: { value: '/derp/1' },
      })
      await fireEvent.click(screen.getByRole('button', { name: 'Attach program' }))
    })

    expect(fake.fsas[0].programs).toEqual(['/derp/1'])
    expect(container.querySelector('.program')).toHaveTextContent('PROG /derp/1')
  })

  it('detach: the FSA Tree drops the detached program', async () => {
    const fake = installFake()
    fake.fsas[0].programs = ['/derp/1']
    const { container } = await renderAt('/ui/fsas', fake)
    const detach = await screen.findByTestId('detach-program-fsa-a')

    await reloadedAfter(fake, async () => {
      await fireEvent.click(detach)
    })

    expect(fake.fsas[0].programs).toEqual([])
    expect(container.querySelector('.program')).toBeNull()
  })

  it('delete: the End Devices assign select no longer offers the deleted FSA', async () => {
    const fake = installFake()
    const { container } = await renderAt('/ui/fsas', fake)

    await reloadedAfter(fake, async () => {
      await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    })
    expect(fake.fsas).toEqual([])

    await goto('devices')
    const options = Array.from(container.querySelectorAll('#assignSel-1 option')).map(
      (o) => (o as HTMLOptionElement).value,
    )
    expect(options).toEqual([''])
  })
})
