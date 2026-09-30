import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import DerControl from './DerControl.svelte'
import * as api from '../lib/api'
import type { DashboardDevice } from '../lib/dashboard'
import type { DERControlCreated, DERControlListResponse, DERProgramListResponse } from '../lib/dercontrol'

const devices: DashboardDevice[] = [
  { sfdi: '167261211635', lfdi: 'D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1', href: '/edev/0', enabled: true },
]

const program: DERProgramListResponse = {
  device: '0',
  programs: [{ href: '/edev/0/fsa/0/derp/0', mRID: 'D2D2D2D2D2D2D2D2', description: 'fixture program', primacy: 0, derControlListHref: '/edev/0/fsa/0/derp/0/derc' }],
}

const emptyPrograms: DERProgramListResponse = { device: '0', programs: [] }
const emptyControls: DERControlListResponse = { device: '0', controls: [] }

// mockApi wires fetchJSON to answer the der-programs and der/controls GETs
// the panel issues once a device and program are picked, and leaves
// postJSON as a bare spy the test configures itself (or never calls, for
// the no-post-before-Confirm assertion).
function mockApi(programsResponse: DERProgramListResponse = program, controlsResponse: DERControlListResponse = emptyControls) {
  const get = vi.spyOn(api, 'fetchJSON').mockImplementation(async (path: string) => {
    if (path.includes('/der-programs')) return { ok: true, data: programsResponse } as never
    if (path.includes('/api/der/controls?')) return { ok: true, data: controlsResponse } as never
    return { ok: false, error: 'unexpected path: ' + path, status: 404 }
  })
  const post = vi.spyOn(api, 'postJSON')
  return { get, post }
}

async function pickDeviceAndProgram(container: HTMLElement) {
  await fireEvent.change(container.querySelector('#controlDevice') as HTMLSelectElement, {
    target: { value: '0' },
  })
  await waitFor(() => {
    expect((container.querySelector('#controlProgram') as HTMLSelectElement).options).toHaveLength(2)
  })
  await fireEvent.change(container.querySelector('#controlProgram') as HTMLSelectElement, {
    target: { value: '/edev/0/fsa/0/derp/0' },
  })
}

async function fillMinimalConnect(container: HTMLElement) {
  await pickDeviceAndProgram(container)
  await fireEvent.input(container.querySelector('#controlDuration') as HTMLInputElement, {
    target: { value: '5' },
  })
}

describe('DerControl: Send/Confirm posts exactly once (criterion 2)', () => {
  it('sends no request on Send; Confirm posts exactly one request', async () => {
    const { post } = mockApi()
    const { container } = render(DerControl, { devices })
    await fillMinimalConnect(container)

    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => {
      expect(screen.getByTestId('der-control-confirm')).toHaveTextContent('Connect, starting now, for 5 min.')
    })
    expect(post).not.toHaveBeenCalled()

    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))

    // A second Confirm click (the button is gone once pendingBody clears,
    // but assert the count stays put rather than trusting disappearance).
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('posts the maxLimW body built from the percent input (criterion 1 wiring)', async () => {
    const { post } = mockApi()
    post.mockResolvedValue({
      ok: true,
      data: {
        mRID: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
        href: '/edev/0/fsa/0/derp/0/derc/c1',
        derProgramHref: program.programs![0]!.href,
        derControlListHref: '/edev/0/fsa/0/derp/0/derc',
        type: 'maxLimW',
        description: '',
        derControlBase: { opModMaxLimW: 4550 },
        creationTime: 1000,
        interval: { start: 1000, duration: 300 },
        eventStatus: { currentStatus: 1, status: 'scheduled', dateTime: 1000 },
        supersedes: [],
        notificationAttempted: true,
        persisted: true,
      } satisfies DERControlCreated,
    })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await fireEvent.change(container.querySelector('#controlType') as HTMLSelectElement, {
      target: { value: 'maxLimW' },
    })
    await fireEvent.input(container.querySelector('#controlValue') as HTMLInputElement, {
      target: { value: '45.5' },
    })
    await fireEvent.input(container.querySelector('#controlDuration') as HTMLInputElement, {
      target: { value: '5' },
    })

    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/der/controls', {
      derProgramHref: program.programs![0]!.href,
      type: 'maxLimW',
      durationSeconds: 300,
      maxLimW: 4550,
    })
  })
})

describe('DerControl: result area (criterion 3)', () => {
  it('on success shows the stored time, mRID, derived status and expected device read time, never "sent"', async () => {
    const { post } = mockApi()
    post.mockResolvedValue({
      ok: true,
      data: {
        mRID: 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB',
        href: '/edev/0/fsa/0/derp/0/derc/c1',
        derProgramHref: program.programs![0]!.href,
        derControlListHref: '/edev/0/fsa/0/derp/0/derc',
        type: 'connect',
        description: '',
        derControlBase: { opModConnect: true },
        creationTime: 0,
        interval: { start: 0, duration: 300 },
        eventStatus: { currentStatus: 1, status: 'scheduled', dateTime: 0 },
        supersedes: [],
        notificationAttempted: true,
        persisted: true,
      } satisfies DERControlCreated,
    })
    const { container } = render(DerControl, { devices })
    await fillMinimalConnect(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))

    const resultEl = container.querySelector('#controlResult') as HTMLElement
    await waitFor(() => expect(resultEl).toHaveTextContent('BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB'))
    expect(resultEl.textContent).toContain('1970-01-01T00:00:00.000Z')
    expect(resultEl.textContent).toContain('scheduled')
    expect(resultEl.textContent).toContain('1970-01-01T00:15:00.000Z') // creationTime(0) + 900s pollRate
    expect(resultEl.textContent).not.toMatch(/\bsent\b/)
  })

  it('on refusal shows the server error text', async () => {
    const { post } = mockApi()
    post.mockResolvedValue({ ok: false, error: 'maxLimW: must be 0 to 10000', status: 400 })
    const { container } = render(DerControl, { devices })
    await fillMinimalConnect(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))

    await waitFor(() => {
      expect(container.querySelector('#controlResult')).toHaveTextContent('maxLimW: must be 0 to 10000')
    })
    expect(container.querySelector('#controlResult')?.textContent).not.toMatch(/\bsent\b/)
  })

  it('on network failure shows an error, not a stored claim', async () => {
    const { post } = mockApi()
    post.mockResolvedValue({ ok: false, error: 'network error', status: 0 })
    const { container } = render(DerControl, { devices })
    await fillMinimalConnect(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))

    await waitFor(() => {
      expect(container.querySelector('#controlResult')).toHaveTextContent('network error')
    })
    expect(container.querySelector('#controlResult')?.textContent).not.toMatch(/\bsent\b/)
  })
})

describe('DerControl: not persisted (criterion 4)', () => {
  it('says a restart forgets the control when persisted is false', async () => {
    const { post } = mockApi()
    post.mockResolvedValue({
      ok: true,
      data: {
        mRID: 'CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC',
        href: '/edev/0/fsa/0/derp/0/derc/c1',
        derProgramHref: program.programs![0]!.href,
        derControlListHref: '/edev/0/fsa/0/derp/0/derc',
        type: 'connect',
        description: '',
        derControlBase: { opModConnect: true },
        creationTime: 0,
        interval: { start: 0, duration: 300 },
        eventStatus: { currentStatus: 1, status: 'scheduled', dateTime: 0 },
        supersedes: [],
        notificationAttempted: true,
        persisted: false,
      } satisfies DERControlCreated,
    })
    const { container } = render(DerControl, { devices })
    await fillMinimalConnect(container)
    await fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))

    await waitFor(() => {
      expect(container.querySelector('#controlResult')).toHaveTextContent('restart')
    })
  })
})

describe('DerControl: controls table and cancel (criterion 5)', () => {
  const listed: DERControlListResponse = {
    device: '0',
    controls: [
      {
        mRID: 'DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD',
        href: '/edev/0/fsa/0/derp/0/derc/c1',
        derProgramHref: program.programs![0]!.href,
        derControlListHref: '/edev/0/fsa/0/derp/0/derc',
        type: 'maxLimW',
        description: '',
        derControlBase: { opModMaxLimW: 4550 },
        creationTime: 0,
        interval: { start: 0, duration: 300 },
        eventStatus: { currentStatus: 1, status: 'scheduled', dateTime: 0 },
        responses: { total: 2, byStatus: { '1': 2 } },
      },
    ],
  }

  it('lists type, value, start, end, status and response counts labelled as device-reported', async () => {
    mockApi(program, listed)
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)

    await waitFor(() => expect(screen.getByTestId('der-control-row')).toBeInTheDocument())
    const row = screen.getByTestId('der-control-row')
    expect(row).toHaveTextContent('maxLimW')
    expect(row).toHaveTextContent('45.50%')
    expect(row).toHaveTextContent('scheduled')
    expect(row).toHaveTextContent('2 reported')
    expect(container.querySelector('table')).toHaveTextContent('not verified by server')
  })

  it('cancels a Scheduled row only after a second confirmation, then reloads the table', async () => {
    const { post, get } = mockApi(program, listed)
    post.mockResolvedValue({
      ok: true,
      data: {
        mRID: 'DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD',
        href: '/edev/0/fsa/0/derp/0/derc/c1',
        derProgramHref: program.programs![0]!.href,
        derControlListHref: '/edev/0/fsa/0/derp/0/derc',
        type: 'maxLimW',
        description: '',
        derControlBase: { opModMaxLimW: 4550 },
        creationTime: 0,
        interval: { start: 0, duration: 300 },
        eventStatus: { currentStatus: 4, status: 'cancelled', dateTime: 0 },
      },
    })
    const { container } = render(DerControl, { devices })
    await pickDeviceAndProgram(container)
    await waitFor(() => expect(screen.getByTestId('der-control-row')).toBeInTheDocument())

    const callsBeforeCancel = get.mock.calls.filter((c) => (c[0] as string).includes('/api/der/controls?')).length

    const cancelBtn = screen.getByTestId('der-control-cancel-DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD')
    await fireEvent.click(cancelBtn)
    // The cancel POST has not happened yet: only the inline confirmation
    // step appeared.
    expect(post).not.toHaveBeenCalled()

    const confirmBtn = screen.getByTestId('der-control-confirm-cancel-DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD')
    await fireEvent.click(confirmBtn)

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/der/controls/DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD/cancel', {})
    // Reload after cancel: a second der/controls GET beyond the initial load.
    await waitFor(() => {
      const callsAfter = get.mock.calls.filter((c) => (c[0] as string).includes('/api/der/controls?')).length
      expect(callsAfter).toBeGreaterThan(callsBeforeCancel)
    })
  })
})

describe('DerControl: no DER programs (criterion 6)', () => {
  it('tells the operator programs come from the boot fixture', async () => {
    mockApi(emptyPrograms)
    const { container } = render(DerControl, { devices })
    await fireEvent.change(container.querySelector('#controlDevice') as HTMLSelectElement, {
      target: { value: '0' },
    })

    await waitFor(() => {
      expect(screen.getByTestId('der-control-no-programs')).toHaveTextContent('boot fixture')
    })
    expect(screen.getByTestId('der-control-no-programs')).toHaveTextContent('cannot be created in the admin UI')
  })
})

describe('DerControl: ids and the removed stub (criterion 7)', () => {
  it('keeps controlType, controlValue and controlResult; drops the stub note', () => {
    mockApi()
    const { container } = render(DerControl, { devices })
    expect(container.querySelector('#controlType')).toBeTruthy()
    expect(container.querySelector('#controlValue')).toBeTruthy()
    expect(container.querySelector('#controlResult')).toBeTruthy()
    expect(container.querySelector('[data-testid="der-control-stub-note"]')).toBeNull()
    expect(container.textContent).not.toContain('admin DER control route')
  })
})
