import { describe, expect, it } from 'vitest'
import { canCancelFsaControl, devicesHolding, fsaControlRequest, fsaControlStatus, holdingText, type FsaControlView } from './fsacontrol'

const held = (status: string) => ({ currentStatus: 1, status, dateTime: 0 })

function view(over: Partial<FsaControlView> = {}): FsaControlView {
  return {
    id: 'c1',
    mRID: 'M',
    type: 'x',
    description: '',
    derControlBase: {},
    creationTime: 0,
    interval: { start: 0, duration: 1 },
    ended: false,
    cancelled: false,
    cancelledAt: null,
    copies: [],
    ...over,
  }
}

describe('fsaControlRequest', () => {
  it('drops kind and keeps every other member', () => {
    expect(fsaControlRequest({ kind: 'scheduled', functions: { a: 1 }, startTime: 5, durationSeconds: 60, description: 'd' })).toEqual({
      functions: { a: 1 },
      startTime: 5,
      durationSeconds: 60,
      description: 'd',
    })
  })
})

describe('copies held', () => {
  const c = view({
    copies: [
      { device: 'a', href: 'h', eventStatus: held('active') },
      { device: 'b', href: 'h', eventStatus: null },
      { device: 'c', href: 'h', eventStatus: held('scheduled') },
    ],
  })

  it('counts the copies with a status out of all assigned devices', () => {
    expect(devicesHolding(c)).toBe(2)
    expect(holdingText(c)).toBe('2 of 3 devices')
  })

  it('lists the distinct statuses, and cancelled or ended win', () => {
    expect(fsaControlStatus(c)).toBe('active, scheduled')
    expect(fsaControlStatus({ ...c, ended: true })).toBe('ended')
    expect(fsaControlStatus({ ...c, ended: true, cancelled: true })).toBe('cancelled')
    expect(fsaControlStatus(view())).toBe('no device holds it')
  })

  it('offers cancel only on a live record', () => {
    expect(canCancelFsaControl(c)).toBe(true)
    expect(canCancelFsaControl({ cancelled: true, ended: false })).toBe(false)
    expect(canCancelFsaControl({ cancelled: false, ended: true })).toBe(false)
  })
})
