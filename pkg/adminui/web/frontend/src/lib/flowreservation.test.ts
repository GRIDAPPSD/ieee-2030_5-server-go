import { describe, expect, it } from 'vitest'
import fixture from './flowreservation.fixture.json'
import {
  directionLabel,
  formatActor,
  formatCountdown,
  formatInterval,
  formatQuantity,
  formatMeasured,
  historyResponses,
  normalizeQueue,
  parseFleetLFDIs,
  remainingSeconds,
  scaledNumber,
  type FlowReservationQueue,
} from './flowreservation'

const q = fixture as unknown as FlowReservationQueue

describe('flowreservation helpers', () => {
  it('applies the multiplier and keeps absent parts as null', () => {
    expect(scaledNumber({ value: 25, multiplier: 3 })).toBe(25000)
    expect(scaledNumber({ value: 5, multiplier: -1 })).toBe(0.5)
    expect(scaledNumber(null)).toBeNull()
    expect(scaledNumber({ value: 5 })).toBeNull()
    expect(scaledNumber({ multiplier: 1 } as never)).toBeNull()
  })

  it('shows a missing quantity as missing, never as 0 or NaN', () => {
    expect(formatQuantity(null, 'Wh')).toBe('missing')
    expect(formatQuantity(undefined, 'Wh')).toBe('missing')
    expect(formatQuantity(NaN, 'Wh')).toBe('missing')
    expect(formatQuantity(0, 'Wh')).toBe('0 Wh')
    expect(formatQuantity(-0.4, 'Wh')).toBe('0 Wh')
  })

  it('prints the value with its wire sign', () => {
    expect(formatQuantity(-8000, 'Wh')).toBe('-8,000 Wh')
    expect(formatQuantity(8000, 'Wh')).toBe('8,000 Wh')
    expect(directionLabel('discharge')).toBe('discharge')
    expect(directionLabel(null)).toBe('direction missing')
    expect(directionLabel('sideways')).toBe('direction missing')
  })

  it('counts down against the server clock', () => {
    const pending = q.requests[0]
    expect(remainingSeconds(pending.deadlineAt, q.now!, 0)).toBe(200)
    expect(remainingSeconds(pending.deadlineAt, q.now!, 150)).toBe(50)
    expect(formatCountdown(pending.deadlineAt, q.now!, 150)).toBe('50s to deadline')
    expect(formatCountdown(pending.deadlineAt, q.now!, 200)).toBe('deadline passed')
    expect(formatCountdown(null, q.now!, 0)).toBe('deadline missing')
    expect(formatCountdown(undefined, q.now!, 0)).toBe('deadline missing')
    expect(formatCountdown(pending.deadlineAt, null, 0)).toBe('server time unavailable')
  })

  it('prints a missing interval as missing', () => {
    expect(formatInterval(null)).toBe('interval missing')
    expect(formatInterval({ start: 1790000000, duration: 900 })).toBe('2026-09-21 14:13:20 UTC for 15 min')
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
    expect(formatActor(undefined)).toBe('unknown')
  })
})

describe('normalizeQueue', () => {
  it('accepts the fixture and reads an undefined tip as null', () => {
    const d = JSON.parse(JSON.stringify(fixture))
    delete d.requests[0].tip
    const r = normalizeQueue(d)
    expect('queue' in r && r.queue.requests[0].tip).toBeNull()
    expect('queue' in normalizeQueue(fixture)).toBe(true)
  })

  it('refuses what the pane cannot render', () => {
    expect(normalizeQueue(null)).toEqual({ error: 'queue is not an object' })
    expect(normalizeQueue([q])).toEqual({ error: 'queue is not an object' })
    expect(normalizeQueue({ requests: 'x' })).toEqual({ error: 'queue has no requests list' })
    const d = JSON.parse(JSON.stringify(fixture))
    d.requests[1].tip.executions = null
    expect(normalizeQueue(d)).toEqual({ error: 'request 1 tip has no executions list' })
  })
})

describe('parseFleetLFDIs', () => {
  it('lists LFDIs and refuses a bad or repeating list', () => {
    expect(parseFleetLFDIs([{ aggregatorLFDI: 'A' }, { aggregatorLFDI: 'B' }])).toEqual(['A', 'B'])
    expect(parseFleetLFDIs([])).toEqual([])
    expect(parseFleetLFDIs(null)).toBeNull()
    expect(parseFleetLFDIs([{ aggregatorLFDI: 'A' }, { aggregatorLFDI: 'A' }])).toBeNull()
    expect(parseFleetLFDIs([{}])).toBeNull()
  })
})

describe('formatMeasured', () => {
  it('keeps one decimal below 10, marks the sub-0.1 as non-zero, and leaves the rest as formatQuantity', () => {
    expect(formatMeasured(0.4, 'Wh')).toBe('0.4 Wh')
    expect(formatMeasured(-9.94, 'Wh')).toBe('-9.9 Wh')
    expect(formatMeasured(0.04, 'W')).toBe('<0.1 W')
    expect(formatMeasured(-0.04, 'W')).toBe('-<0.1 W')
    expect(formatMeasured(0, 'Wh')).toBe('0 Wh')
    expect(formatMeasured(1234.4, 'Wh')).toBe('1,234 Wh')
    expect(formatMeasured(10, 'Wh')).toBe('10 Wh')
    expect(formatMeasured(null, 'Wh')).toBe('missing')
  })
})
