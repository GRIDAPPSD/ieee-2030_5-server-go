import { describe, expect, it } from 'vitest'
import {
  buildControlRequest,
  canCancel,
  controlValueText,
  expectedDeviceReadTime,
  fmtTime,
  keptControl,
  type ControlFormInput,
  type DERControlView,
} from './dercontrol'

// base is a valid input for every field buildControlRequest does not
// vary per test, so each test overrides only what it is about.
const base: ControlFormInput = {
  deviceLabel: '222222222222',
  programLabel: 'fixture program',
  programHref: '/edev/0/fsa/0/derp/0',
  type: 'connect',
  value: null,
  excitation: false,
  startNow: true,
  startAtLocal: '',
  durationMinutes: 5,
  description: '',
}

describe('buildControlRequest: posted body per type (criterion 1)', () => {
  it('connect: no value fields', () => {
    const res = buildControlRequest({ ...base, type: 'connect' })
    expect(res).toEqual({
      ok: true,
      body: { derProgramHref: base.programHref, type: 'connect', durationSeconds: 300 },
      summary: 'Connect on device 222222222222, program fixture program, starting now, for 5 min.',
    })
  })

  it('disconnect: no value fields', () => {
    const res = buildControlRequest({ ...base, type: 'disconnect' })
    expect(res).toEqual({
      ok: true,
      body: { derProgramHref: base.programHref, type: 'disconnect', durationSeconds: 300 },
      summary: 'Disconnect on device 222222222222, program fixture program, starting now, for 5 min.',
    })
  })

  it('maxLimW: percent sent in hundredths', () => {
    const res = buildControlRequest({ ...base, type: 'maxLimW', value: 45.5 })
    expect(res).toEqual({
      ok: true,
      body: { derProgramHref: base.programHref, type: 'maxLimW', durationSeconds: 300, maxLimW: 4550 },
      summary: 'Limit to 45.50% of setMaxW on device 222222222222, program fixture program, starting now, for 5 min.',
    })
  })

  it('maxLimW: out of range is refused client-side, nothing built', () => {
    const res = buildControlRequest({ ...base, type: 'maxLimW', value: 150 })
    expect(res).toEqual({ ok: false, error: 'Limit must be 0 to 100 percent.' })
  })

  it('fixedPFInjectW: power factor sent in thousandths, with excitation', () => {
    const res = buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 0.95, excitation: true })
    expect(res).toEqual({
      ok: true,
      body: {
        derProgramHref: base.programHref,
        type: 'fixedPFInjectW',
        durationSeconds: 300,
        powerFactor: { displacement: 950, excitation: true },
      },
      summary: 'Set power factor to 0.950 (under-excited) on device 222222222222, program fixture program, starting now, for 5 min.',
    })
  })

  it('fixedPFInjectW: excitation false reads as over-excited', () => {
    const res = buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 0.5, excitation: false })
    expect(res.ok && res.summary).toContain('over-excited')
  })

  it('fixedPFInjectW: out of range is refused client-side', () => {
    const res = buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 0.0001 })
    expect(res).toEqual({ ok: false, error: 'Power factor must be 0.001 to 1.000.' })
  })

  it('a chosen start time is sent as Unix seconds', () => {
    const res = buildControlRequest({
      ...base,
      startNow: false,
      startAtLocal: '2026-01-01T00:00:00',
    })
    // A datetime-local value carries no zone: it is the browser's local time.
    expect(res.ok && res.body.startTime).toBe(Math.floor(new Date(2026, 0, 1, 0, 0, 0).getTime() / 1000))
  })

  it('optional description passes through under the server bound', () => {
    const res = buildControlRequest({ ...base, description: 'short note' })
    expect(res.ok && res.body.description).toBe('short note')
  })

  it('a description over 32 octets is refused client-side', () => {
    const res = buildControlRequest({ ...base, description: 'x'.repeat(33) })
    expect(res).toEqual({ ok: false, error: 'Description must be at most 32 octets.' })
  })

  it('no program selected is refused before duration or type are checked', () => {
    const res = buildControlRequest({ ...base, programHref: '' })
    expect(res).toEqual({ ok: false, error: 'Pick a DER program first.' })
  })

  it('a non-positive duration is refused', () => {
    const res = buildControlRequest({ ...base, durationMinutes: 0 })
    expect(res).toEqual({ ok: false, error: 'Duration must be a positive number of minutes.' })
  })
})

describe('buildControlRequest: rounding and bounds (fix round items 4)', () => {
  it('refuses a limit with more than 2 decimals rather than rounding it silently', () => {
    const res = buildControlRequest({ ...base, type: 'maxLimW', value: 45.555 })
    expect(res).toEqual({ ok: false, error: 'Limit allows at most 2 decimals.' })
  })

  it('refuses a power factor with more than 3 decimals', () => {
    const res = buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 0.1235 })
    expect(res).toEqual({ ok: false, error: 'Power factor allows at most 3 decimals.' })
  })

  it('summary and body carry the same value for a float-noisy limit and power factor', () => {
    const lim = buildControlRequest({ ...base, type: 'maxLimW', value: 0.29 })
    expect(lim.ok && lim.body.maxLimW).toBe(29)
    expect(lim.ok && lim.summary).toContain('Limit to 0.29%')
    const pf = buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 0.145 })
    expect(pf.ok && pf.body.powerFactor?.displacement).toBe(145)
    expect(pf.ok && pf.summary).toContain('power factor to 0.145')
  })

  it('accepts the limit bounds 0 and 100 and refuses just below 0 and above 100', () => {
    const zero = buildControlRequest({ ...base, type: 'maxLimW', value: 0 })
    expect(zero.ok && zero.body.maxLimW).toBe(0)
    const full = buildControlRequest({ ...base, type: 'maxLimW', value: 100 })
    expect(full.ok && full.body.maxLimW).toBe(10000)
    expect(buildControlRequest({ ...base, type: 'maxLimW', value: -0.01 })).toEqual({
      ok: false,
      error: 'Limit must be 0 to 100 percent.',
    })
    expect(buildControlRequest({ ...base, type: 'maxLimW', value: 100.01 }).ok).toBe(false)
  })

  it('accepts the power factor bounds 0.001 and 1 and refuses 0 and above 1', () => {
    const lo = buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 0.001 })
    expect(lo.ok && lo.body.powerFactor?.displacement).toBe(1)
    const hi = buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 1 })
    expect(hi.ok && hi.body.powerFactor?.displacement).toBe(1000)
    expect(buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 0 }).ok).toBe(false)
    expect(buildControlRequest({ ...base, type: 'fixedPFInjectW', value: 1.001 }).ok).toBe(false)
  })

  it('refuses a duration that rounds to 0 seconds and accepts one second', () => {
    expect(buildControlRequest({ ...base, durationMinutes: 0.004 })).toEqual({
      ok: false,
      error: 'Duration must be at least 1 second.',
    })
    const one = buildControlRequest({ ...base, durationMinutes: 1 / 60 })
    expect(one.ok && one.body.durationSeconds).toBe(1)
  })

  it('states a duration that is not whole minutes in seconds, from the posted value', () => {
    const res = buildControlRequest({ ...base, durationMinutes: 0.5 })
    expect(res.ok && res.body.durationSeconds).toBe(30)
    expect(res.ok && res.summary).toContain('for 30 s.')
  })
})

describe('keptControl', () => {
  it('reads mRID and href from a controlKept body', () => {
    expect(keptControl({ error: 'x', mRID: 'AB', href: '/h', controlKept: true })).toEqual({ mRID: 'AB', href: '/h' })
  })
  it('is null when controlKept is false, absent or the fields are not strings', () => {
    expect(keptControl({ error: 'x', mRID: 'AB', href: '/h', controlKept: false })).toBeNull()
    expect(keptControl({ error: 'x' })).toBeNull()
    expect(keptControl({ controlKept: true, mRID: 1, href: '/h' })).toBeNull()
    expect(keptControl(undefined)).toBeNull()
    expect(keptControl('text')).toBeNull()
  })
})

describe('display helpers', () => {
  it('fmtTime renders Unix seconds as an ISO string', () => {
    expect(fmtTime(0)).toBe('1970-01-01T00:00:00.000Z')
  })

  it('expectedDeviceReadTime adds the 900s pollRate', () => {
    expect(expectedDeviceReadTime(0)).toBe(fmtTime(900))
  })

  it('controlValueText renders maxLimW hundredths as a percent', () => {
    const ctrl = { type: 'maxLimW', derControlBase: { opModMaxLimW: 4550 } } as DERControlView
    expect(controlValueText(ctrl)).toBe('45.50%')
  })

  it('controlValueText renders fixedPFInjectW thousandths with excitation', () => {
    const ctrl = {
      type: 'fixedPFInjectW',
      derControlBase: { opModFixedPFInjectW: { displacement: 950, excitation: true, multiplier: -3 } },
    } as DERControlView
    expect(controlValueText(ctrl)).toBe('0.950 (under-excited)')
  })

  it('controlValueText has nothing to show for connect/disconnect', () => {
    const ctrl = { type: 'connect', derControlBase: { opModConnect: true } } as DERControlView
    expect(controlValueText(ctrl)).toBe('-')
  })

  it('canCancel: scheduled always; active only while its interval has not ended', () => {
    expect(canCancel('scheduled', 100, 60, 0)).toBe(true)
    expect(canCancel('active', 100, 60, 159)).toBe(true)
    expect(canCancel('active', 100, 60, 160)).toBe(false)
    expect(canCancel('active', 100, 60, 1000)).toBe(false)
    expect(canCancel('cancelled', 100, 60, 0)).toBe(false)
    expect(canCancel('superseded', 100, 60, 0)).toBe(false)
    expect(canCancel('unknown', 100, 60, 0)).toBe(false)
  })
})
