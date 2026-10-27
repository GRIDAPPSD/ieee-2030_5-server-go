import { describe, expect, it } from 'vitest'
import { canCancel, expectedDeviceReadTime, fmtTime, keptControl, localDateTimeSeconds, startPickerValue } from './dercontrol'

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

  it('canCancel: scheduled always; active only while its interval has not ended on the server clock', () => {
    expect(canCancel('scheduled', 100, 60, 0)).toBe(true)
    expect(canCancel('active', 100, 60, 159)).toBe(true)
    expect(canCancel('active', 100, 60, 160)).toBe(false)
    expect(canCancel('active', 100, 60, 1000)).toBe(false)
    expect(canCancel('cancelled', 100, 60, 0)).toBe(false)
    expect(canCancel('superseded', 100, 60, 0)).toBe(false)
    expect(canCancel('unknown', 100, 60, 0)).toBe(false)
  })

  it('canCancel: with no server time an active row keeps Cancel, so the server decides', () => {
    expect(canCancel('active', 100, 60, null)).toBe(true)
    expect(canCancel('cancelled', 100, 60, null)).toBe(false)
  })
})

describe('start picker helpers', () => {
  const now = new Date(2030, 0, 2, 3, 4, 5).getTime()

  it('startPickerValue opens at the current moment when empty or already passed', () => {
    expect(startPickerValue('', now)).toBe('2030-01-02T03:04:05')
    expect(startPickerValue('2030-01-02T03:04:04', now)).toBe('2030-01-02T03:04:05')
  })

  it('startPickerValue keeps a start that has not passed', () => {
    expect(startPickerValue('2030-01-02T04:00:00', now)).toBe('2030-01-02T04:00:00')
  })

  it('localDateTimeSeconds reads the local time in epoch seconds, and null for a non-date', () => {
    expect(localDateTimeSeconds('2030-01-02T03:04:05')).toBe(now / 1000)
    expect(localDateTimeSeconds('')).toBeNull()
    expect(localDateTimeSeconds('soon')).toBeNull()
  })
})
