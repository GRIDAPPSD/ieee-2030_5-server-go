import { describe, expect, it, vi, afterEach } from 'vitest'
import * as api from './api'
import {
  buildDispatch,
  describeCreateFailure,
  encodeTarget,
  fleetDeviceChoices,
  grantPrefill,
  parseGrants,
  submitDispatch,
  type GrantView,
} from './dispatch'

afterEach(() => {
  vi.restoreAllMocks()
})

function response(over: Record<string, unknown> = {}) {
  return {
    id: 'frq-1-r1',
    href: '/edev/4/frp/frq-1-r1',
    mRID: '9D1E3B5C00000000000000000000A1B2',
    subject: 'S',
    creationTime: 1790000000,
    interval: { start: 1790000000, duration: 1800 },
    energyAvailable: { value: 4000, multiplier: 0 },
    powerAvailable: { value: 8000, multiplier: 0 },
    direction: 'discharge',
    eventStatus: { currentStatus: 1, status: 'active', dateTime: 1790000000 },
    cancelReason: null,
    answeredBy: { kind: 'operator', admission: null, principal: null, at: 1790000000 },
    cancelledBy: null,
    executions: [],
    energyCommittedWh: 1000,
    energyRemainingWh: 3000,
    ...over,
  }
}

function grantWire(over: Record<string, unknown> = {}) {
  return { edevId: '4', frqId: 'frq-1', response: response(), suggestedTargetW: { value: 8000, multiplier: 0 }, ...over }
}

describe('encodeTarget', () => {
  it.each([
    ['8000', { value: 8000, multiplier: 0 }],
    ['-5000', { value: -5000, multiplier: 0 }],
    ['0', { value: 0, multiplier: 0 }],
    ['40000', { value: 4000, multiplier: 1 }],
    ['-40000', { value: -4000, multiplier: 1 }],
  ])('encodes %s with its sign unchanged', (text, want) => {
    expect(encodeTarget(text)).toEqual({ ok: true, target: want })
  })

  it.each(['', 'abc', '+5', '1.5', '40001', '99999999999999999999'])('refuses %j', (text) => {
    expect(encodeTarget(text).ok).toBe(false)
  })
})

describe('buildDispatch', () => {
  const base = { programHref: '/edev/4/derp/1', start: '', duration: '1800', power: '8000', grantMRID: '' }

  it('builds a plain dispatch with no start and no grant key', () => {
    const r = buildDispatch(base)
    expect(r).toEqual({
      ok: true,
      body: { derProgramHref: '/edev/4/derp/1', type: 'targetW', targetW: { value: 8000, multiplier: 0 }, durationSeconds: 1800 },
    })
    expect('executesGrant' in (r as { body: object }).body).toBe(false)
  })

  it('carries the grant mRID and the UTC start as epoch seconds', () => {
    const r = buildDispatch({ ...base, start: '2026-10-01T12:00:00Z', grantMRID: 'ABC', power: '-5000' })
    expect(r).toMatchObject({
      ok: true,
      body: { startTime: Date.UTC(2026, 9, 1, 12) / 1000, executesGrant: 'ABC', targetW: { value: -5000, multiplier: 0 } },
    })
  })

  it.each([
    [{ programHref: '' }, 'program'],
    [{ duration: '0' }, 'Duration'],
    [{ duration: '1.5' }, 'Duration'],
    [{ power: '' }, 'Target power'],
    [{ start: '2026-02-31T00:00:00Z' }, 'Start'],
    [{ start: 'tomorrow' }, 'Start'],
  ])('refuses %j before anything is sent', (over, word) => {
    const r = buildDispatch({ ...base, ...over })
    expect(r.ok).toBe(false)
    expect((r as { error: string }).error).toContain(word)
  })
})

describe('grantPrefill', () => {
  const grant = (over: Partial<GrantView>): GrantView => {
    const parsed = parseGrants({ grants: [grantWire()] })
    if (!('list' in parsed)) throw new Error(parsed.error)
    return { ...parsed.list.grants[0], ...over }
  }
  // The grant runs 1790000000 for 1800 s.
  const BEFORE = 1789990000

  it('fills the interval and the suggested target from a grant that has not started', () => {
    expect(grantPrefill(grant({}), BEFORE)).toEqual({ start: '2026-09-21T14:13:20Z', duration: '1800', power: '8000', note: '' })
  })

  it('keeps the sign the server sent for a charge grant', () => {
    expect(grantPrefill(grant({ suggestedTargetW: { value: -5000, multiplier: 0 } }), BEFORE).power).toBe('-5000')
  })

  it('leaves a value the server did not send empty, never a default', () => {
    const g = grant({ suggestedTargetW: null })
    g.response = { ...g.response, interval: null }
    expect(grantPrefill(g, BEFORE)).toEqual({ start: '', duration: '', power: '', note: '' })
  })

  it('moves a started grant to now plus the margin, runs it to the grant end, and says so', () => {
    const r = grantPrefill(grant({}), 1790000100)
    expect(r.start).toBe('2026-09-21T14:15:30Z')
    expect(r.duration).toBe('1670')
    expect(r.note).toContain('already started')
  })

  it('never prefills a start in the past, and says a grant that is ending has no interval', () => {
    const r = grantPrefill(grant({}), 1790001790)
    expect(r).toMatchObject({ start: '', duration: '' })
    expect(r.note).toContain('ended')
  })
})

describe('parseGrants', () => {
  it('reads the list and the server time', () => {
    const parsed = parseGrants({ aggregatorLFDI: 'A', now: 1790000100, grants: [grantWire()] })
    expect(parsed).toMatchObject({ list: { aggregatorLFDI: 'A', now: 1790000100 } })
    const list = (parsed as { list: { grants: GrantView[] } }).list
    expect(list.grants[0].response.energyRemainingWh).toBe(3000)
    expect(list.grants[0].response.mRID).toBe('9D1E3B5C00000000000000000000A1B2')
  })

  it('reads an absent nullable key as null so it shows as missing', () => {
    const r = response()
    delete (r as Record<string, unknown>).energyRemainingWh
    const parsed = parseGrants({ grants: [grantWire({ response: r })] })
    expect((parsed as { list: { grants: GrantView[] } }).list.grants[0].response.energyRemainingWh).toBeNull()
  })

  it.each([
    [null, 'not an object'],
    [{}, 'no grants list'],
    [{ grants: [null] }, 'not an object'],
    [{ grants: [{ edevId: 4 }] }, 'no edevId'],
    [{ grants: [grantWire({ response: response({ mRID: '' }) })] }, 'no mRID'],
    [{ grants: [grantWire(), grantWire()] }, 'repeat'],
  ])('refuses a malformed reply %#', (data, word) => {
    const parsed = parseGrants(data)
    expect('error' in parsed && parsed.error).toContain(word)
  })
})

describe('fleetDeviceChoices', () => {
  it('maps fleet LFDIs to dashboard ids ignoring case, and leaves unknown devices out', () => {
    const fleetDevices = [
      { lfdi: 'AAAA', measurements: {} },
      { lfdi: 'BBBB', measurements: {} },
    ]
    const dash = [
      { sfdi: 's-a', lfdi: 'aaaa', href: '/edev/4' },
      { sfdi: 's-z', lfdi: 'zzzz', href: '/edev/9' },
    ]
    expect(fleetDeviceChoices(fleetDevices, dash, (h) => h.split('/').pop() as string)).toEqual([{ id: '4', label: 's-a', lfdi: 'aaaa' }])
  })
})

describe('describeCreateFailure', () => {
  it('shows a 409 with the server text, its code and the mRID it names', () => {
    const r = describeCreateFailure(409, 'targetW: exceeds the grant\'s powerAvailable', { error: 'x', code: 'power_exceeds', mRID: 'GRANT1' })
    expect(r.kind).toBe('refused')
    expect(r.message).toContain('targetW: exceeds the grant\'s powerAvailable')
    expect(r.message).toContain('(power_exceeds)')
    expect(r.message).toContain('Names GRANT1.')
  })

  it('treats no answer and a 5xx as an unknown outcome', () => {
    expect(describeCreateFailure(0, 'network error', undefined).kind).toBe('unknown')
    expect(describeCreateFailure(500, 'internal error', { error: 'internal error' }).kind).toBe('unknown')
  })

  it('names a kept control from a 500 so the operator can find it', () => {
    const r = describeCreateFailure(500, 'e', { controlKept: true, mRID: 'KEPT1', href: '/edev/4/derp/1/derc/9' })
    expect(r.kind).toBe('unknown')
    expect(r.message).toContain('KEPT1')
    expect(r.message).toContain('/edev/4/derp/1/derc/9')
  })

  it('keeps a 400 refusal as refused', () => {
    expect(describeCreateFailure(400, 'duration: out of range', { code: 'duration_range' }).kind).toBe('refused')
  })
})

describe('submitDispatch', () => {
  const created = {
    mRID: 'NEW1',
    href: '/edev/4/derp/1/derc/1',
    derProgramHref: '/edev/4/derp/1',
    derControlListHref: '/edev/4/derp/1/derc',
    type: 'targetW',
    description: '',
    derControlBase: { opModTargetW: { value: 8000, multiplier: 0 } },
    creationTime: 1790000000,
    interval: { start: 1790000000, duration: 1800 },
    eventStatus: { currentStatus: 0, status: 'scheduled', dateTime: 1790000000 },
    executesGrant: 'G1',
    supersedes: [],
    notificationAttempted: true,
    persisted: true,
  }

  it('posts the body as given and returns the stored control', async () => {
    const post = vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: created })
    const body = { derProgramHref: '/edev/4/derp/1', type: 'targetW' as const, targetW: { value: 8000, multiplier: 0 }, durationSeconds: 1800 }
    const r = await submitDispatch(body, {})
    expect(post).toHaveBeenCalledWith('/api/der/controls', body, {})
    expect(r).toEqual({ ok: true, control: created })
  })

  it('reads an unreadable 2xx reply as an unknown outcome', async () => {
    vi.spyOn(api, 'postJSON').mockResolvedValue({ ok: true, data: { mRID: 'x' } })
    const r = await submitDispatch({ derProgramHref: 'h', type: 'targetW', targetW: { value: 1, multiplier: 0 }, durationSeconds: 1 }, {})
    expect(r).toMatchObject({ ok: false, kind: 'unknown' })
  })
})
