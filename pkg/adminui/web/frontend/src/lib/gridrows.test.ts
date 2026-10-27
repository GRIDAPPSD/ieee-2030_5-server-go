// The pure side of the grid block entry: the request body per row (what a
// chained row and a typed start each send), the confirm line per row, the
// rows that cannot be sent, and the server's refusals worded per row.
import { describe, expect, it, vi, afterEach } from 'vitest'
import * as api from './api'
import {
  addRow,
  buildGridRequest,
  MAX_GRID_ROWS,
  newRow,
  parseGridCreated,
  pickerValue,
  refusedRows,
  refusalText,
  removeRow,
  submitGridRequest,
  type GridRowDraft,
} from './gridrows'

const at = (h: number, m = 0) => new Date(2030, 0, 2, h, m, 0)
const local = (d: Date) => d.getTime() / 1000

function filled(over: Partial<GridRowDraft> = {}): GridRowDraft {
  return { ...newRow(), direction: 'discharge', x: '50', y: '3', durationMinutes: '60', ...over }
}

afterEach(() => vi.restoreAllMocks())

describe('buildGridRequest', () => {
  it('sends a start-now row with no start and no followsPrevious', () => {
    const r = buildGridRequest([filled()])
    expect(r.ok).toBe(true)
    if (r.ok) expect(r.body).toEqual({ rows: [{ direction: 'discharge', x: 50, y: 3, durationSeconds: 3600 }] })
  })

  it('sends a chained row as followsPrevious with no start, and a typed start as its epoch seconds without followsPrevious', () => {
    const rows = addRow([filled()])
    const chained = buildGridRequest(rows)
    if (!chained.ok) throw new Error('chained batch refused')
    expect(chained.body.rows[1]).toEqual({ direction: 'discharge', x: 50, y: 3, durationSeconds: 3600, followsPrevious: true })
    expect(chained.body.rows[1]).not.toHaveProperty('start')

    rows[1] = { ...rows[1], follows: false, startNow: false, start: '2030-01-02T14:00:00' }
    const typed = buildGridRequest(rows)
    if (!typed.ok) throw new Error('typed batch refused')
    expect(typed.body.rows[1]).toEqual({ direction: 'discharge', x: 50, y: 3, durationSeconds: 3600, start: local(at(14)) })
    expect(typed.body.rows[1]).not.toHaveProperty('followsPrevious')
  })

  it('states each row direction in words and its signed wire value, discharge positive and charge negative', () => {
    const r = buildGridRequest([filled(), filled({ direction: 'charge', x: '20', follows: true, startNow: false })])
    if (!r.ok) throw new Error('batch refused')
    expect(r.lines[0].directionWord).toBe('DISCHARGE')
    expect(r.lines[0].text).toBe(
      'Block 1: DISCHARGE 50,000 W TO the grid, starts now, for 60 min. Wire: opModTargetW = +50 x 10^3 W (positive = discharge).',
    )
    expect(r.lines[1].directionWord).toBe('CHARGE')
    expect(r.lines[1].text).toBe(
      'Block 2: CHARGE 20,000 W FROM the grid, starts when block 1 ends, for 60 min. Wire: opModTargetW = -20 x 10^3 W (negative = charge).',
    )
  })

  it('words a typed start as a local interval', () => {
    const r = buildGridRequest([filled({ startNow: false, start: '2030-01-02T14:00:00' })])
    if (!r.ok) throw new Error('batch refused')
    expect(r.lines[0].text).toContain('from 2030-01-02 14:00:00 to 2030-01-02 15:00:00 local')
  })

  it('refuses a row with no direction, and builds nothing from a partly valid batch', () => {
    const r = buildGridRequest([filled(), filled({ direction: '' })])
    expect(r).toEqual({ ok: false, errors: [{ row: 2, message: 'Choose a direction: to the grid or from the grid.' }] })
  })

  it.each([
    ['-5', 'The amount takes no sign. Choose the direction instead.'],
    ['+5', 'The amount takes no sign. Choose the direction instead.'],
    ['', 'Enter the amount.'],
    ['1.5', 'The amount is a whole number.'],
    ['0', 'The amount is 1 to 999,999,999.'],
    ['1000000000', 'The amount is 1 to 999,999,999.'],
  ])('refuses the amount %j', (x, message) => {
    expect(buildGridRequest([filled({ x })])).toEqual({ ok: false, errors: [{ row: 1, message }] })
  })

  it('refuses a multiplier outside -9..9 and a duration outside 1..1440 minutes', () => {
    expect(buildGridRequest([filled({ y: '10' })]).ok).toBe(false)
    expect(buildGridRequest([filled({ y: '-9' })]).ok).toBe(true)
    expect(buildGridRequest([filled({ durationMinutes: '0' })]).ok).toBe(false)
    expect(buildGridRequest([filled({ durationMinutes: '1441' })]).ok).toBe(false)
    expect(buildGridRequest([filled({ durationMinutes: '1440' })]).ok).toBe(true)
  })
})

describe('rows', () => {
  it('addRow copies direction, amount and duration, chains to the row before, and stops at the cap', () => {
    const rows = addRow([filled({ direction: 'charge', x: '7', y: '2', durationMinutes: '15' })])
    expect(rows[1]).toEqual({ direction: 'charge', x: '7', y: '2', durationMinutes: '15', startNow: false, start: '', follows: true })
    let many = [filled()]
    for (let i = 0; i < 40; i++) many = addRow(many)
    expect(many).toHaveLength(MAX_GRID_ROWS)
  })

  it('removeRow of the first row makes the next one start now instead of chaining to nothing', () => {
    const rows = removeRow(addRow([filled()]), 0)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({ follows: false, startNow: true, start: '' })
  })

  it('pickerValue shows a chained row at the end of the row before it, from the start-now anchor', () => {
    const rows = addRow([filled({ durationMinutes: '30' })])
    expect(pickerValue(rows, 1, at(10).getTime())).toBe('2030-01-02T10:30:00')
    expect(pickerValue(addRow(rows), 2, at(10).getTime())).toBe('2030-01-02T11:00:00')
  })

  it('pickerValue is empty while the row before has no valid duration', () => {
    expect(pickerValue(addRow([filled({ durationMinutes: '' })]), 1, at(10).getTime())).toBe('')
  })
})

describe('refusals', () => {
  it('words an overlap with another row, and with an earlier batch with its window', () => {
    expect(refusalText({ row: 2, code: 'overlapping_request', conflictsWith: { row: 1 } })).toBe('It overlaps block 1 of this batch. (overlapping_request)')
    expect(refusalText({ row: 2, code: 'overlapping_request', conflictsWith: { row: 0, batch: 'ab12', start: 0, end: 60 } })).toBe(
      'It overlaps a live block of batch ab12 (its row 0, 1970-01-01T00:00:00.000Z to 1970-01-01T00:01:00.000Z). (overlapping_request)',
    )
  })

  it('shows an unknown code as given', () => {
    expect(refusalText({ row: 1, code: 'something_new' })).toBe('The server refused it. (something_new)')
  })

  it('groups every refusal of a row under its number', () => {
    const refusal = {
      code: 'rows_conflict',
      error: 'x',
      devices: [],
      rows: [
        { row: 2, code: 'overlapping_request' },
        { row: 4, code: 'start_in_past' },
        { row: 2, code: 'too_many_events' },
      ],
    }
    const m = refusedRows(refusal)
    expect([...m.keys()]).toEqual([2, 4])
    expect(m.get(2)).toHaveLength(2)
    expect(m.get(4)).toEqual(['The start is in the past. (start_in_past)'])
  })
})

describe('parseGridCreated', () => {
  const created = {
    batch: 'b1',
    persisted: true,
    warnings: ['1 device used the 5 kW test default'],
    rows: [{ row: 1, direction: 'discharge', requestedW: 50000, encodedW: 50000, differenceW: 0, shares: [{ aggregatorLFDI: 'A', devices: 2, targetW: { value: 50, multiplier: 3 }, issued: true }] }],
  }

  it('reads the figures the page shows, and null when one is missing', () => {
    expect(parseGridCreated(created)).toEqual({ batch: 'b1', persisted: true, warnings: created.warnings, rows: [{ ...created.rows[0], shares: created.rows[0].shares }] })
    expect(parseGridCreated({ ...created, rows: [{ ...created.rows[0], encodedW: undefined }] })).toBeNull()
    expect(parseGridCreated({ ...created, batch: 3 })).toBeNull()
  })

  it('reads a null warnings list as none', () => {
    expect(parseGridCreated({ ...created, warnings: null })?.warnings).toEqual([])
  })
})

describe('submitGridRequest', () => {
  it('is a refusal for a 4xx, carrying the rows, and an unknown outcome for a 5xx and for no answer', async () => {
    const body = { rows: [{ direction: 'discharge' as const, x: 1, y: 0, durationSeconds: 60 }] }
    const post = vi.spyOn(api, 'postJSON')
    post.mockResolvedValueOnce({ ok: false, status: 409, error: 'one or more rows were refused', body: { error: 'one or more rows were refused', code: 'rows_conflict', rows: [{ row: 1, code: 'overlapping_request' }] } })
    const refused = await submitGridRequest(body, {})
    expect(refused).toMatchObject({ ok: false, kind: 'refused', refusal: { code: 'rows_conflict', rows: [{ row: 1, code: 'overlapping_request' }] } })

    post.mockResolvedValueOnce({ ok: false, status: 500, error: 'boom' })
    expect(await submitGridRequest(body, {})).toMatchObject({ ok: false, kind: 'unknown' })
    post.mockResolvedValueOnce({ ok: false, status: 0, error: 'network error' })
    expect(await submitGridRequest(body, {})).toMatchObject({ ok: false, kind: 'unknown' })
  })

  it('names the shares a failed undo kept', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValueOnce({ ok: false, status: 500, error: 'x', body: { error: 'x', batch: 'b9', kept: [{ mRID: 'M1', cancelHref: '/c/1' }] } })
    const r = await submitGridRequest({ rows: [] }, {})
    expect(r).toMatchObject({ ok: false, kind: 'unknown' })
    if (!r.ok) expect(r.message).toContain('M1 (/c/1)')
  })
})
