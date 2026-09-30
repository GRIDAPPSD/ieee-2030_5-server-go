import { describe, expect, it } from 'vitest'
import {
  buildControlRequest,
  canCancel,
  controlValueText,
  expectedDeviceReadTime,
  fmtTime,
  type ControlFormInput,
  type DERControlView,
} from './dercontrol'

// base is a valid input for every field buildControlRequest does not
// vary per test, so each test overrides only what it is about.
const base: ControlFormInput = {
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
      summary: 'Connect, starting now, for 5 min.',
    })
  })

  it('disconnect: no value fields', () => {
    const res = buildControlRequest({ ...base, type: 'disconnect' })
    expect(res).toEqual({
      ok: true,
      body: { derProgramHref: base.programHref, type: 'disconnect', durationSeconds: 300 },
      summary: 'Disconnect, starting now, for 5 min.',
    })
  })

  it('maxLimW: percent sent in hundredths', () => {
    const res = buildControlRequest({ ...base, type: 'maxLimW', value: 45.5 })
    expect(res).toEqual({
      ok: true,
      body: { derProgramHref: base.programHref, type: 'maxLimW', durationSeconds: 300, maxLimW: 4550 },
      summary: 'Limit to 45.50% of setMaxW, starting now, for 5 min.',
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
      summary: 'Set power factor to 0.950 (under-excited), starting now, for 5 min.',
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
      startAtLocal: '2026-01-01T00:00:00.000Z',
    })
    expect(res.ok && res.body.startTime).toBe(1767225600)
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

  it('canCancel allows scheduled and active, not ended states', () => {
    expect(canCancel('scheduled')).toBe(true)
    expect(canCancel('active')).toBe(true)
    expect(canCancel('cancelled')).toBe(false)
    expect(canCancel('superseded')).toBe(false)
    expect(canCancel('unknown')).toBe(false)
  })
})
