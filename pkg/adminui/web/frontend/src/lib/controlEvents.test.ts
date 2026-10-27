// The events timeline's narrowing and layout. Every assertion is on a value
// the chart draws: the parsed fields, the lane order, the bar percentages.
import { describe, expect, it } from 'vitest'
import { barsFor, forgetLookAhead, lookAheadFor, rememberLookAhead, controlEventsPath, eventLabel, eventLabelParts, eventTitle, lanesFor, nowPercent, parseControlEvents } from './controlEvents'
import { END, wireEvent, wireEvents } from './controlEvents.fixture'
import { parsePower } from './power'
import { wireAnswer } from './power.fixture'

const RS = (END - 3600) * 1000
const RE = END * 1000

describe('parseControlEvents', () => {
  it('keeps every field of a good reply', () => {
    const got = parseControlEvents(wireEvents([wireEvent({ origin: 'derms', fsaID: 'F1', scope: 'fsa' })]))
    expect(got?.windowStart).toBe(END - 3600)
    expect(got?.events[0]).toMatchObject({ group: 'AGG1', origin: 'derms', fsaID: 'F1', scope: 'fsa', targetW: 5000, status: 'active' })
  })

  it('reads the look-ahead as seconds past the window end, and null when absent', () => {
    expect(parseControlEvents(wireEvents([], END + 3600))?.aheadSeconds).toBe(3600)
    expect(parseControlEvents(wireEvents([], null))?.aheadSeconds).toBeNull()
    expect(parseControlEvents(wireEvents([], END - 1))).toBeNull()
    expect(parseControlEvents({ ...wireEvents([]), aheadEnd: 'x' })).toBeNull()
  })

  it('accepts an unassigned event with no device ids instead of refusing the reply', () => {
    const got = parseControlEvents(wireEvents([wireEvent({ group: 'unassigned', groupKind: 'unassigned', deviceIDs: [] }), wireEvent({ mrid: 'b' })]))
    expect(got?.events.map((e) => [e.group, e.groupKind, e.deviceIDs])).toEqual([
      ['unassigned', 'unassigned', []],
      ['AGG1', 'aggregator', ['D1']],
    ])
  })

  it('reads status unknown and a null modes as none instead of refusing the reply', () => {
    const wire = wireEvents([{ ...wireEvent({ mrid: 'a', status: 'unknown' }), modes: null } as never, wireEvent({ mrid: 'b' })])
    const got = parseControlEvents(wire)
    expect(got?.events.map((e) => [e.mrid, e.status, e.modes])).toEqual([
      ['a', 'unknown', []],
      ['b', 'active', ['opModTargetW']],
    ])
  })

  it('refuses the whole reply when one event has an unknown status or a missing field', () => {
    expect(parseControlEvents(wireEvents([wireEvent(), { ...wireEvent(), status: 'paused' } as never]))).toBeNull()
    const { mrid: _drop, ...noMrid } = wireEvent()
    expect(parseControlEvents(wireEvents([noMrid as never]))).toBeNull()
    expect(parseControlEvents({ windowStart: 1, windowEnd: 2 })).toBeNull()
    expect(parseControlEvents(null)).toBeNull()
  })

  it('asks for the whole fleet over the chosen window', () => {
    expect(controlEventsPath(3600)).toBe('/api/derms/control-events?window=3600&scope=all')
  })
})

describe('eventLabel', () => {
  it('states direction and magnitude in words, or the modes without a target', () => {
    expect(eventLabel(wireEvent({ targetW: 5000, direction: 'discharge' }))).toBe('discharge 5.0 kW')
    expect(eventLabel(wireEvent({ targetW: -3000, direction: 'charge' }))).toBe('charge 3.0 kW')
    expect(eventLabel(wireEvent({ targetW: null, direction: null, modes: ['opModConnect', 'opModEnergize'] }))).toBe('opModConnect, opModEnergize')
    expect(eventLabel(wireEvent({ targetW: null, direction: null, modes: [] }))).toBe('control')
  })

  it('splits the direction word from the target so a narrow bar keeps the word', () => {
    expect(eventLabelParts(wireEvent({ targetW: -3000, direction: 'charge' }))).toEqual({ lead: 'charge', detail: ' 3.0 kW', word: true })
    expect(eventLabelParts(wireEvent({ targetW: null, direction: null, modes: ['opModConnect'] }))).toEqual({ lead: 'opModConnect', detail: '', word: false })
    expect(eventLabelParts(wireEvent({ targetW: null, direction: 'discharge', modes: [] }))).toEqual({ lead: 'discharge', detail: '', word: true })
  })

  it('puts the status in the hover text', () => {
    expect(eventTitle(wireEvent({ status: 'cancelled', origin: 'derms' }))).toContain('discharge 5.0 kW, DERMS, cancelled,')
  })
})

describe('lanesFor', () => {
  const answer = (over: Record<string, unknown>) => {
    const a = parsePower(wireAnswer(over))
    if (a === null) throw new Error('fixture did not parse')
    return a
  }
  const energy = (key: string) => ({ key, name: key, latestW: null, latestMissing: [], discharged: [], charged: [] })
  const labelOf = (l: string) => `name-${l}`

  it('follows the power chart order with Direct where the power chart puts it', () => {
    const lanes = lanesFor(answer({ aggregatorEnergy: [energy('B'), energy('A'), energy('direct')] }), [], labelOf)
    expect(lanes).toEqual([
      { key: 'B', label: 'name-B' },
      { key: 'A', label: 'name-A' },
      { key: 'direct', label: 'Direct' },
    ])
  })

  it('puts Unassigned last, after Direct and an unlisted group, only when an event is in it', () => {
    const a = answer({ aggregatorEnergy: [energy('A'), energy('direct')] })
    const un = wireEvent({ group: 'unassigned', groupKind: 'unassigned', deviceIDs: [] })
    expect(lanesFor(a, [un, wireEvent({ group: 'Z' })], labelOf)).toEqual([
      { key: 'A', label: 'name-A' },
      { key: 'direct', label: 'Direct' },
      { key: 'Z', label: 'name-Z' },
      { key: 'unassigned', label: 'Unassigned' },
    ])
    expect(lanesFor(a, [], labelOf).map((l) => l.key)).toEqual(['A', 'direct'])
  })

  it('falls back to the aggregators then Direct, and adds a lane for an unlisted group', () => {
    const lanes = lanesFor(answer({ aggregators: ['A'], aggregatorEnergy: null }), [wireEvent({ group: 'Z' })], labelOf)
    expect(lanes.map((l) => l.key)).toEqual(['A', 'direct', 'Z'])
  })
})

describe('barsFor', () => {
  it('places a bar by its start and end as a percent of the range', () => {
    const [bar] = barsFor([wireEvent({ start: END - 1800, end: END - 900 })], RS, RE)
    expect(bar.left).toBeCloseTo(50, 6)
    expect(bar.width).toBeCloseTo(25, 6)
    expect(bar.row).toBe(0)
  })

  it('clips to the range and drops events wholly outside it', () => {
    const bars = barsFor(
      [wireEvent({ mrid: 'in', start: END - 7200, end: END - 1800 }), wireEvent({ mrid: 'out', start: END - 9000, end: END - 7200 })],
      RS,
      RE,
    )
    expect(bars.map((b) => b.event.mrid)).toEqual(['in'])
    expect(bars[0].left).toBe(0)
    expect(bars[0].width).toBeCloseTo(50, 6)
  })

  it('stacks overlapping events into rows and reuses a row once it is free', () => {
    const bars = barsFor(
      [
        wireEvent({ mrid: 'a', start: END - 3000, end: END - 2000 }),
        wireEvent({ mrid: 'b', start: END - 2500, end: END - 1500 }),
        wireEvent({ mrid: 'c', start: END - 2000, end: END - 1000 }),
      ],
      RS,
      RE,
    )
    expect(bars.map((b) => [b.event.mrid, b.row])).toEqual([
      ['a', 0],
      ['b', 1],
      ['c', 0],
    ])
  })
})

describe('nowPercent', () => {
  it('is the position in the range, held at the end past it, and null before it', () => {
    expect(nowPercent(RS + 1800_000, RS, RE)).toBeCloseTo(50, 6)
    expect(nowPercent(RE + 5000, RS, RE)).toBe(100)
    expect(nowPercent(RS - 1, RS, RE)).toBeNull()
  })
})

describe('look-ahead memory', () => {
  it('is zero until a server has answered with one, then one window', () => {
    forgetLookAhead()
    expect(lookAheadFor(900)).toBe(0)
    rememberLookAhead(true)
    expect(lookAheadFor(900)).toBe(900)
    expect(lookAheadFor(3600)).toBe(3600)
    rememberLookAhead(false)
    expect(lookAheadFor(900)).toBe(0)
  })
})
