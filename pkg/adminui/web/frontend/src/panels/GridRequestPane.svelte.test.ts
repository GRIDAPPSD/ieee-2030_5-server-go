// The grid request pane against a mocked create. What it must get right:
// nothing is sent without a direction or with a signed amount, and nothing
// before Confirm; "Add row" sends followsPrevious and typing a start drops it;
// the confirm step restates every row's direction and signed wire value; a
// refused batch marks each named row and keeps what was typed; the server's
// warnings and shares are shown after a 201; an unknown outcome is never
// re-sent.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import GridRequestPane from './GridRequestPane.svelte'
import * as api from '../lib/api'

type Res =
  | { ok: true; data: unknown; serverTime?: number }
  | { ok: false; error: string; status: number; body?: unknown }

const AGG = 'AGG00000000000000000000000000000000001'

function created(rows = 1, warnings: string[] = []) {
  return {
    batch: 'b0b1b2b3b4b5',
    persisted: true,
    notificationAttempted: true,
    warnings,
    rows: Array.from({ length: rows }, (_, n) => ({
      row: n + 1,
      direction: n === 1 ? 'charge' : 'discharge',
      requestedW: n === 1 ? -20000 : 50000,
      encodedW: n === 1 ? -20000 : 50000,
      differenceW: 0,
      shares: [{ aggregatorLFDI: AGG, devices: 2, targetW: { value: n === 1 ? -20 : 50, multiplier: 3 }, issued: true }],
    })),
  }
}

function mockWrite(res: Res | Promise<Res>) {
  return vi.spyOn(api, 'postJSON').mockImplementation((async () => res) as never)
}

const rowsOf = () => screen.getAllByTestId('grid-row')

async function fillRow(n: number, v: { dir?: 'To grid' | 'From grid'; x?: string; y?: string; minutes?: string }) {
  const row = within(rowsOf()[n])
  if (v.dir === 'To grid') await fireEvent.click(row.getByRole('radio', { name: /^To grid/ }))
  if (v.dir === 'From grid') await fireEvent.click(row.getByRole('radio', { name: /^From grid/ }))
  if (v.x !== undefined) await fireEvent.input(row.getByTestId('grid-x'), { target: { value: v.x } })
  if (v.y !== undefined) await fireEvent.input(row.getByTestId('grid-y'), { target: { value: v.y } })
  if (v.minutes !== undefined) await fireEvent.input(row.getByTestId('grid-duration'), { target: { value: v.minutes } })
}

const review = () => fireEvent.click(screen.getByRole('button', { name: 'Review' }))
const confirmButton = () => screen.getByRole('button', { name: 'Confirm' })

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('GridRequestPane entry', () => {
  it('has no direction chosen to start with, and refuses to review a row without one', async () => {
    const post = mockWrite({ ok: true, data: created() })
    render(GridRequestPane)
    const radios = within(rowsOf()[0]).getAllByRole('radio') as HTMLInputElement[]
    expect(radios.map((r) => r.checked)).toEqual([false, false])
    await fillRow(0, { x: '50', minutes: '60' })
    await review()
    expect(screen.queryByTestId('grid-confirm')).toBeNull()
    expect(within(rowsOf()[0]).getByTestId('grid-row-refusal')).toHaveTextContent('Choose a direction')
    expect(post).not.toHaveBeenCalled()
  })

  it('refuses an amount with a sign, naming the direction as the way to say it', async () => {
    const post = mockWrite({ ok: true, data: created() })
    render(GridRequestPane)
    await fillRow(0, { dir: 'From grid', x: '-50', minutes: '60' })
    await review()
    expect(within(rowsOf()[0]).getByTestId('grid-row-refusal')).toHaveTextContent('The amount takes no sign')
    expect(screen.queryByTestId('grid-confirm')).toBeNull()
    expect(post).not.toHaveBeenCalled()
  })

  it('"Add row" chains the new row to the end of the last, and sends followsPrevious', async () => {
    const post = mockWrite({ ok: true, data: created(2) })
    render(GridRequestPane)
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    expect(rowsOf()).toHaveLength(2)
    const second = within(rowsOf()[1])
    expect((second.getAllByRole('radio') as HTMLInputElement[]).map((r) => r.checked)).toEqual([false, false])
    await fillRow(1, { dir: 'To grid' })
    expect(second.getByTestId('grid-follows')).toHaveTextContent('Starts when block 1 ends')
    expect((second.getByTestId('grid-start') as HTMLInputElement).value).not.toBe('')
    expect((second.getByTestId('grid-x') as HTMLInputElement).value).toBe('50')
    await review()
    await fireEvent.click(confirmButton())
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post.mock.calls[0][0]).toBe('/api/derms/grid-requests')
    expect(post.mock.calls[0][1]).toEqual({
      rows: [
        { direction: 'discharge', x: 50, y: 3, durationSeconds: 3600 },
        { direction: 'discharge', x: 50, y: 3, durationSeconds: 3600, followsPrevious: true },
      ],
    })
  })

  it('typing a start into a chained row clears followsPrevious and sends the typed start', async () => {
    const post = mockWrite({ ok: true, data: created(2) })
    render(GridRequestPane)
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    await fillRow(1, { dir: 'To grid' })
    await fireEvent.input(within(rowsOf()[1]).getByTestId('grid-start'), { target: { value: '2031-03-04T05:06:07' } })
    expect(within(rowsOf()[1]).queryByTestId('grid-follows')).toBeNull()
    await review()
    await fireEvent.click(confirmButton())
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    const sent = (post.mock.calls[0][1] as { rows: Record<string, unknown>[] }).rows[1]
    expect(sent).toEqual({ direction: 'discharge', x: 50, y: 3, durationSeconds: 3600, start: new Date(2031, 2, 4, 5, 6, 7).getTime() / 1000 })
    expect(sent).not.toHaveProperty('followsPrevious')
  })

  it('tells the page once a batch is stored, and not when it is refused', async () => {
    const onCreated = vi.fn()
    const post = mockWrite({ ok: true, data: created(1) })
    render(GridRequestPane, { onCreated })
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await review()
    expect(onCreated).not.toHaveBeenCalled()
    await fireEvent.click(confirmButton())
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('does not tell the page when the server refuses the batch', async () => {
    const onCreated = vi.fn()
    const post = mockWrite({ ok: false, status: 409, error: 'rows_conflict', body: { code: 'rows_conflict', error: 'rows_conflict', rows: [] } })
    render(GridRequestPane, { onCreated })
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await review()
    await fireEvent.click(confirmButton())
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    await screen.findByTestId('grid-form-error')
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('an added row cannot be reviewed until a direction is chosen on it', async () => {
    const post = mockWrite({ ok: true, data: created(2) })
    render(GridRequestPane)
    await fillRow(0, { dir: 'From grid', x: '50', minutes: '60' })
    await fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    await review()
    expect(screen.queryByTestId('grid-confirm')).toBeNull()
    expect(rowsOf()[0]).not.toContainElement(screen.queryByTestId('grid-row-refusal'))
    expect(within(rowsOf()[1]).getByTestId('grid-row-refusal')).toHaveTextContent('Choose a direction')
    expect(post).not.toHaveBeenCalled()
  })

  it('keeps the amount unsigned for a charge row', async () => {
    const post = mockWrite({ ok: true, data: created() })
    render(GridRequestPane)
    await fillRow(0, { dir: 'From grid', x: '-20', minutes: '60' })
    await review()
    expect(within(rowsOf()[0]).getByTestId('grid-row-refusal')).toHaveTextContent('The amount takes no sign')
    await fillRow(0, { x: '20' })
    await review()
    await fireEvent.click(confirmButton())
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect((post.mock.calls[0][1] as { rows: unknown[] }).rows).toEqual([{ direction: 'charge', x: 20, y: 3, durationSeconds: 3600 }])
  })

  describe('with the clock fixed', () => {
    afterEach(() => vi.useRealTimers())

    it('opens the start picker at the current moment when Start now is unchecked, and keeps a later start typed there', async () => {
      vi.useFakeTimers({ toFake: ['Date'] })
      vi.setSystemTime(new Date(2030, 0, 2, 3, 4, 5))
      render(GridRequestPane)
      const row = within(rowsOf()[0])
      expect(row.queryByTestId('grid-start')).toBeNull()
      await fireEvent.click(row.getByRole('checkbox', { name: 'Start now' }))
      // jsdom may append milliseconds to a datetime-local value; a browser does not.
      expect((row.getByTestId('grid-start') as HTMLInputElement).value).toMatch(/^2030-01-02T03:04:05(\.000)?$/)
      await fireEvent.input(row.getByTestId('grid-start'), { target: { value: '2030-01-03T06:00:00' } })
      await fireEvent.click(row.getByRole('checkbox', { name: 'Start now' }))
      await fireEvent.click(row.getByRole('checkbox', { name: 'Start now' }))
      expect((row.getByTestId('grid-start') as HTMLInputElement).value).toMatch(/^2030-01-03T06:00(:00)?(\.000)?$/)
    })
  })

  it('removing a block drops it and keeps the rest as typed', async () => {
    render(GridRequestPane)
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    await fillRow(1, { x: '9' })
    await fireEvent.click(screen.getByRole('button', { name: 'Remove block 1' }))
    expect(rowsOf()).toHaveLength(1)
    expect((within(rowsOf()[0]).getByTestId('grid-x') as HTMLInputElement).value).toBe('9')
    expect(within(rowsOf()[0]).queryByTestId('grid-follows')).toBeNull()
  })
})

describe('GridRequestPane confirm', () => {
  it('sends nothing before Confirm, and restates every row direction in words with its signed wire value', async () => {
    const post = mockWrite({ ok: true, data: created(2) })
    render(GridRequestPane)
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    await fillRow(1, { dir: 'From grid', x: '20' })
    await review()
    const lines = screen.getAllByTestId('grid-confirm-line').map((l) => l.textContent)
    expect(lines).toEqual([
      'Block 1: DISCHARGE 50,000 W TO the grid, starts now, for 60 min. Wire, batch total: opModTargetW = +50 x 10^3 W (positive = discharge).',
      'Block 2: CHARGE 20,000 W FROM the grid, starts when block 1 ends, for 60 min. Wire, batch total: opModTargetW = -20 x 10^3 W (negative = charge).',
    ])
    expect(post).not.toHaveBeenCalled()
  })

  it('marks each confirm line by its direction, discharge and charge differently', async () => {
    mockWrite({ ok: true, data: created(2) })
    render(GridRequestPane)
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    await fillRow(1, { dir: 'From grid' })
    await review()
    const lines = screen.getAllByTestId('grid-confirm-line')
    expect(lines[0]).toHaveClass('dir-discharge')
    expect(lines[0]).not.toHaveClass('dir-charge')
    expect(lines[1]).toHaveClass('dir-charge')
    expect(lines[1]).not.toHaveClass('dir-discharge')
  })

  it('Back returns to the rows as typed', async () => {
    mockWrite({ ok: true, data: created() })
    render(GridRequestPane)
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await review()
    await fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect((within(rowsOf()[0]).getByTestId('grid-x') as HTMLInputElement).value).toBe('50')
    expect((within(rowsOf()[0]).getByRole('radio', { name: /^To grid/ }) as HTMLInputElement).checked).toBe(true)
  })
})

describe('GridRequestPane after the answer', () => {
  async function twoRows() {
    render(GridRequestPane)
    await fillRow(0, { dir: 'To grid', x: '50', minutes: '60' })
    await fireEvent.click(screen.getByRole('button', { name: 'Add row' }))
    await fillRow(1, { dir: 'From grid', x: '20' })
    await review()
    await fireEvent.click(confirmButton())
  }

  it('shows the shares and the warnings after a 201, and starts a fresh row', async () => {
    mockWrite({ ok: true, data: created(2, ['1 device used the 5 kW test default']) })
    await twoRows()
    expect(await screen.findByTestId('grid-created')).toHaveTextContent('Stored batch b0b1b2b3b4b5 with 2 blocks.')
    const shares = screen.getAllByTestId('grid-share').map((s) => s.textContent)
    expect(shares).toEqual([expect.stringContaining('2 devices: 50,000 W (opModTargetW)'), expect.stringContaining('2 devices: -20,000 W (opModTargetW)')])
    expect(screen.getByTestId('grid-warnings')).toHaveTextContent('1 device used the 5 kW test default')
    expect(rowsOf()).toHaveLength(1)
    expect((within(rowsOf()[0]).getByTestId('grid-x') as HTMLInputElement).value).toBe('')
  })

  const conflict = {
    error: 'one or more rows were refused',
    code: 'rows_conflict',
    rows: [{ row: 2, code: 'overlapping_request', conflictsWith: { row: 1 } }],
  }

  it.each([
    ['409 rows_conflict', 409, conflict, 'It overlaps block 1 of this batch. (overlapping_request)'],
    [
      '422 rows_invalid',
      422,
      { error: 'one or more rows are invalid', code: 'rows_invalid', rows: [{ row: 2, code: 'start_in_past' }] },
      'The start is in the past. (start_in_past)',
    ],
    [
      '409 rows_refused',
      409,
      { error: 'refused', code: 'rows_refused', rows: [{ row: 2, code: 'commitment_conflict' }] },
      'The server refused it. (commitment_conflict)',
    ],
    [
      '409 too_many_events',
      409,
      { error: 'too many', code: 'too_many_events', rows: [{ row: 2, code: 'too_many_events', aggregatorLFDI: AGG }], devices: [{ device: '7', aggregatorLFDI: AGG, live: 23, added: 2 }] },
      'It would take a device past 24 live controls. (too_many_events)',
    ],
    [
      '409 aggregator_unavailable',
      409,
      {
        error: 'unavailable',
        code: 'aggregator_unavailable',
        rows: [{ row: 2, code: 'aggregator_unavailable', aggregatorLFDI: AGG }],
        aggregators: [{ aggregatorLFDI: AGG, device: '7', code: 'device_not_assignable' }],
      },
      'Aggregator AGG0000000000000... cannot take it: device 7 (device_not_assignable). (aggregator_unavailable)',
    ],
  ])('marks the row a %s names, and only that row, and keeps what was typed', async (_label, status, body, text) => {
    mockWrite({ ok: false, status, error: body.error, body })
    await twoRows()
    const marked = await screen.findAllByTestId('grid-row-refusal')
    expect(marked).toHaveLength(1)
    expect(rowsOf()[1]).toContainElement(marked[0])
    expect(marked[0]).toHaveTextContent('Refused: ' + text)
    expect(screen.getByTestId('grid-form-error')).toHaveTextContent(body.code)
    expect((within(rowsOf()[0]).getByTestId('grid-x') as HTMLInputElement).value).toBe('50')
    expect((within(rowsOf()[1]).getByTestId('grid-x') as HTMLInputElement).value).toBe('20')
    expect((within(rowsOf()[1]).getByRole('radio', { name: /^From grid/ }) as HTMLInputElement).checked).toBe(true)
    expect(within(rowsOf()[1]).getByTestId('grid-follows')).toBeInTheDocument()
  })

  it('lists the devices a too_many_events refusal names', async () => {
    const body = { error: 'too many', code: 'too_many_events', rows: [{ row: 2, code: 'too_many_events' }], devices: [{ device: '7', aggregatorLFDI: AGG, live: 23, added: 2 }] }
    mockWrite({ ok: false, status: 409, error: body.error, body })
    await twoRows()
    expect(await screen.findByTestId('grid-devices')).toHaveTextContent('Device 7')
    expect(screen.getByTestId('grid-devices')).toHaveTextContent('23 live, 2 added')
  })

  it('shows a refusal that names no row on the form, and marks none', async () => {
    const body = { error: 'no aggregator with a managed device is available', code: 'no_aggregators', rows: [] }
    mockWrite({ ok: false, status: 409, error: body.error, body })
    await twoRows()
    expect(await screen.findByTestId('grid-form-error')).toHaveTextContent('no_aggregators')
    expect(screen.queryByTestId('grid-row-refusal')).toBeNull()
    expect(rowsOf()).toHaveLength(2)
  })

  it('locks sending after an unknown outcome until the operator says the Controls tab was checked', async () => {
    const post = mockWrite({ ok: false, status: 500, error: 'boom' })
    await twoRows()
    expect(await screen.findByTestId('grid-unknown')).toHaveTextContent('Outcome unknown')
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    expect(post).toHaveBeenCalledTimes(1)
    await fireEvent.click(screen.getByRole('button', { name: /allow sending again/ }))
    expect(screen.getByRole('button', { name: 'Review' })).toBeEnabled()
    expect((within(rowsOf()[1]).getByTestId('grid-x') as HTMLInputElement).value).toBe('20')
  })
})
