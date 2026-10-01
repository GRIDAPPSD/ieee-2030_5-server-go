import { describe, expect, it } from 'vitest'
import fixture from './flowreservation.fixture.json'
import {
  directionLabel,
  formatActor,
  formatCountdown,
  formatQuantity,
  historyResponses,
  normalizeQueues,
  remainingSeconds,
  scaledNumber,
  type FlowReservationQueue,
} from './flowreservation'

const q = fixture as unknown as FlowReservationQueue

describe('flowreservation helpers', () => {
  it('applies the multiplier and keeps null as null', () => {
    expect(scaledNumber({ value: 25, multiplier: 3 })).toBe(25000)
    expect(scaledNumber({ value: 5, multiplier: -1 })).toBe(0.5)
    expect(scaledNumber(null)).toBeNull()
  })

  it('shows a missing quantity as missing, never as 0', () => {
    expect(formatQuantity(null, 'Wh')).toBe('missing')
    expect(formatQuantity(0, 'Wh')).toBe('0 Wh')
  })

  it('prints the magnitude and leaves direction to the server word', () => {
    expect(formatQuantity(-8000, 'Wh')).toBe('8,000 Wh')
    expect(directionLabel('discharge')).toBe('discharge')
    expect(directionLabel(null)).toBe('direction missing')
    expect(directionLabel('sideways')).toBe('direction missing')
  })

  it('counts down against the server clock from the fixture', () => {
    const pending = q.requests[0]
    expect(remainingSeconds(pending.deadlineAt, q.now!, 0)).toBe(200)
    expect(remainingSeconds(pending.deadlineAt, q.now!, 150)).toBe(50)
    expect(formatCountdown(50)).toBe('50s to deadline')
    expect(formatCountdown(0)).toBe('deadline passed')
    expect(remainingSeconds(null, q.now!, 0)).toBeNull()
    expect(formatCountdown(null)).toBe('deadline missing')
  })

  it('splits the chain into the tip and the earlier responses', () => {
    const granted = q.requests[1]
    expect(granted.tip?.id).toBe('frq-1789990000000000002-r1')
    expect(historyResponses(granted).map((r) => r.id)).toEqual(['frq-1789990000000000002'])
    expect(historyResponses(q.requests[0])).toEqual([])
  })

  it('names who answered and who cancelled', () => {
    const history = historyResponses(q.requests[1])[0]
    expect(formatActor(history.answeredBy)).toBe('deadline fallback')
    expect(formatActor(history.cancelledBy)).toBe('operator cert:5D9A0C1B7E3F2A4... via mtls')
    expect(formatActor(null)).toBe('unknown')
  })

  it('rejects a body that is not a queue', () => {
    expect(normalizeQueues(null)).toBeNull()
    expect(normalizeQueues({ requests: 'x' })).toBeNull()
    expect(normalizeQueues(q)).toEqual([q])
    expect(normalizeQueues([q])).toEqual([q])
  })
})
