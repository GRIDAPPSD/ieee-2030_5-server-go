// What the write helpers must get right: the body carries magnitudes and the
// operator's choice and never a sign; each action goes to its own route; an
// input the page cannot send is refused before any request; and every
// refusal from the server's table reads as plain words with its code and the
// ids the body carries.
import { describe, expect, it } from 'vitest'
import {
  buildAction,
  formatStart,
  submitWrite,
  describeRefusal,
  emptyForm,
  REASON_MAX,
  type ActionForm,
} from './flowreservationWrite'
import type { FlowReservationEntry } from './flowreservation'
import fixture from './flowreservation.fixture.json'

const ENTRY = fixture.requests[0] as unknown as FlowReservationEntry
const BASE = '/api/derms/flow-reservations/4/frq-1790000000000000001/'

function form(over: Partial<ActionForm>): ActionForm {
  return { ...emptyForm(), ...over }
}

describe('buildAction', () => {
  it('grants as asked with only the decision, to the answer route', () => {
    expect(buildAction('grant_as_asked', ENTRY, emptyForm())).toEqual({
      ok: true,
      path: BASE + 'answer',
      body: { decision: 'grant' },
    })
  })

  it('denies through the answer route', () => {
    expect(buildAction('deny', ENTRY, emptyForm())).toEqual({
      ok: true,
      path: BASE + 'answer',
      body: { decision: 'deny' },
    })
  })

  it('sends an adjusted grant with magnitudes and a zero multiplier', () => {
    const built = buildAction(
      'grant_adjusted',
      ENTRY,
      form({ start: '2026-10-01T12:00:00Z', duration: '900', energy: '5000', power: '2000' }),
    )
    expect(built).toEqual({
      ok: true,
      path: BASE + 'answer',
      body: {
        decision: 'grant',
        interval: { start: Date.parse('2026-10-01T12:00:00Z') / 1000, duration: 900 },
        energy: { value: 5000, multiplier: 0 },
        power: { value: 2000, multiplier: 0 },
      },
    })
  })

  it('leaves a blank field out of the body so the server reads it as as asked', () => {
    const built = buildAction('grant_adjusted', ENTRY, form({ power: '1500' }))
    expect(built).toEqual({
      ok: true,
      path: BASE + 'answer',
      body: { decision: 'grant', power: { value: 1500, multiplier: 0 } },
    })
  })

  it.each(['-5', '-0', '+5', '5.5', '1e3', 'abc', '0x10'])('refuses energy %s instead of sending or flipping it', (energy) => {
    const built = buildAction('grant_adjusted', ENTRY, form({ energy }))
    expect(built.ok).toBe(false)
  })

  it('refuses an adjusted grant that sets nothing', () => {
    const built = buildAction('grant_adjusted', ENTRY, emptyForm())
    expect(built).toEqual({ ok: false, error: 'set an interval, energy or power, or use Grant as asked' })
  })

  it.each([
    ['2026-10-01 12:00', '900'],
    ['2026-10-01T12:00:00+02:00', '900'],
    ['', '900'],
    ['2026-10-01T12:00:00Z', ''],
    ['2026-10-01T12:00:00Z', '0'],
    ['2026-10-01T12:00:00Z', '-60'],
    ['2026-13-45T12:00:00Z', '900'],
  ])('refuses interval start %s duration %s', (start, duration) => {
    expect(buildAction('grant_adjusted', ENTRY, form({ start, duration, energy: '1' })).ok).toBe(false)
  })

  it('revises through the revise route with the reason', () => {
    const built = buildAction('revise', ENTRY, form({ energy: '4000', reason: 'feeder limit' }))
    expect(built).toEqual({
      ok: true,
      path: BASE + 'revise',
      body: { decision: 'grant', energy: { value: 4000, multiplier: 0 }, reason: 'feeder limit' },
    })
  })

  it('revises to a denial without carrying any quantity', () => {
    const built = buildAction('revise', ENTRY, form({ deny: true, energy: '4000', power: '9', reason: 'no room' }))
    expect(built).toEqual({ ok: true, path: BASE + 'revise', body: { decision: 'deny', reason: 'no room' } })
  })

  it('cancels with an empty body, or with the reason when one is given', () => {
    expect(buildAction('cancel', ENTRY, emptyForm())).toEqual({ ok: true, path: BASE + 'cancel', body: {} })
    expect(buildAction('cancel', ENTRY, form({ reason: 'fault' }))).toEqual({
      ok: true,
      path: BASE + 'cancel',
      body: { reason: 'fault' },
    })
  })

  it('accepts a reason of exactly the limit and refuses one more character', () => {
    expect(buildAction('cancel', ENTRY, form({ reason: 'a'.repeat(REASON_MAX) })).ok).toBe(true)
    expect(buildAction('cancel', ENTRY, form({ reason: 'a'.repeat(REASON_MAX + 1) })).ok).toBe(false)
  })

  it.each(['2026-02-31T12:00:00Z', '2026-04-31T12:00:00Z', '2026-10-01T24:00:00Z', '2026-10-01T12:60:00Z', '2026-10-01T12:00:61Z'])(
    'refuses the impossible start %s instead of rolling it over',
    (start) => {
      expect(buildAction('grant_adjusted', ENTRY, form({ start, duration: '60' })).ok).toBe(false)
    },
  )

  it('accepts a real leap day and a start without seconds', () => {
    expect(buildAction('grant_adjusted', ENTRY, form({ start: '2028-02-29T12:00:00Z', duration: '60' })).ok).toBe(true)
    expect(buildAction('grant_adjusted', ENTRY, form({ start: '2026-10-01T12:00Z', duration: '60' })).ok).toBe(true)
  })

  it('addresses the request by the server ids, escaped', () => {
    const odd = { ...ENTRY, edevId: 'a/b', frqId: 'x y' }
    const built = buildAction('grant_as_asked', odd, emptyForm())
    expect(built.ok && built.path).toBe('/api/derms/flow-reservations/a%2Fb/x%20y/answer')
  })
})

describe('describeRefusal', () => {
  it('shows a 503 not_configured as answers not enabled', () => {
    expect(describeRefusal(503, 'x', { error: 'x', code: 'not_configured' })).toBe(
      'Answers are not enabled on this server (not_configured).',
    )
  })

  it.each([404, 405])('shows a %i with no refusal body as an API that is not there yet', (status) => {
    expect(describeRefusal(status, 'request failed with status ' + status, undefined)).toBe(
      'Answer API not available on this server yet.',
    )
  })

  it('does not read a 404 request_not_found as a missing route', () => {
    const text = describeRefusal(404, 'no request', { error: 'no request', code: 'request_not_found', frqId: 'frq-9' })
    expect(text).toContain('request_not_found')
    expect(text).toContain('frq-9')
    expect(text).not.toContain('not available')
  })

  it('names the mRID and request id a 409 carries, with the code', () => {
    const text = describeRefusal(409, 'conflict', {
      error: 'conflict',
      code: 'fleet_window_committed',
      mRID: 'ABCD1234',
      frqId: 'frq-7',
    })
    expect(text).toBe(
      'The new grant overlaps a grant or control the fleet already has. (fleet_window_committed) Names mRID ABCD1234, request frq-7.',
    )
  })

  it.each([
    [400, 'interval_outside_window', 'outside the window'],
    [400, 'energy_exceeds_request', 'more than the aggregator asked'],
    [409, 'already_answered', 'already has an answer'],
    [409, 'grant_not_live', 'not live'],
    [409, 'execution_exceeds_energy', 'more than the new energy'],
    [409, 'execution_reverses_grant', 'opposite direction'],
  ])('maps %i %s to plain words and keeps the code', (status, code, words) => {
    const text = describeRefusal(status, 'server text', { error: 'server text', code })
    expect(text).toContain(words)
    expect(text).toContain('(' + code + ')')
  })

  it('shows an unknown code with the server own text', () => {
    expect(describeRefusal(409, 'something new', { error: 'something new', code: 'brand_new' })).toBe(
      'something new (brand_new)',
    )
  })

  it('reads a timeout as unknown outcome, not as a refusal', () => {
    expect(describeRefusal(0, 'request timed out', undefined)).toContain('Outcome unknown: reloading')
  })

  it.each([
    [500, 'internal'],
    [502, null],
    [503, 'unavailable'],
    [504, null],
  ])('reads a %i (%s) as outcome unknown, never as a stored failure', (status, code) => {
    const text = describeRefusal(status, 'x', code === null ? undefined : { error: 'x', code })
    expect(text).toContain('Outcome unknown: reloading')
    expect(text).toContain('answered ' + status)
  })
})

describe('formatStart', () => {
  it('prints a normal start and returns blank for one outside the Date range', () => {
    expect(formatStart(1790003600)).toBe(new Date(1790003600000).toISOString().replace('.000Z', 'Z'))
    expect(formatStart(1e15)).toBe('')
  })
})

describe('submitWrite', () => {
  const HREF = ENTRY.requestHref
  const other = () => ({ ...JSON.parse(JSON.stringify(ENTRY)), requestHref: '/edev/9/frq/other' })

  it('refuses a reply for another request as unknown, and one without ids', async () => {
    const { vi } = await import('vitest')
    const api = await import('./api')
    const spy = vi.spyOn(api, 'postJSON')
    spy.mockResolvedValueOnce({ ok: true, data: other() })
    const wrong = await submitWrite('/x', {}, HREF, {})
    expect(wrong.ok).toBe(false)
    expect(!wrong.ok && wrong.kind).toBe('unknown')
    const noIds = JSON.parse(JSON.stringify(ENTRY))
    delete noIds.frqId
    spy.mockResolvedValueOnce({ ok: true, data: noIds })
    expect((await submitWrite('/x', {}, HREF, {})).ok).toBe(false)
    spy.mockResolvedValueOnce({ ok: true, data: JSON.parse(JSON.stringify(ENTRY)) })
    expect((await submitWrite('/x', {}, HREF, {})).ok).toBe(true)
    spy.mockRestore()
  })

  it.each([
    [409, 'already_answered', 'stale'],
    [409, 'grant_not_live', 'stale'],
    [409, 'request_cancelled', 'stale'],
    [409, 'not_answered', 'stale'],
    [404, 'request_not_found', 'stale'],
    [409, 'fleet_window_committed', 'refused'],
    [400, 'interval_outside_window', 'refused'],
    [503, 'not_configured', 'refused'],
    [500, 'internal', 'unknown'],
    [502, '', 'unknown'],
    [504, '', 'unknown'],
    [0, '', 'unknown'],
  ])('classifies %i %s as %s', async (status, code, kind) => {
    const { vi } = await import('vitest')
    const api = await import('./api')
    const spy = vi.spyOn(api, 'postJSON').mockResolvedValueOnce({
      ok: false,
      status,
      error: 'e',
      body: code === '' ? undefined : { error: 'e', code },
    })
    const r = await submitWrite('/x', {}, HREF, {})
    spy.mockRestore()
    expect(!r.ok && r.kind).toBe(kind)
  })
})
