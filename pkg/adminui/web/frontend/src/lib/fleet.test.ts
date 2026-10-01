// The trap this covers (#671 acceptance criteria, carried from #715's
// FleetSum contract): a roll-up over devices that did not report shows the
// count missing, never a filled value. sumFigure is what stands between a
// raw FleetSum and the pane, so its 'none' branch is the property under
// test, not just that it renders without throwing.
import { describe, expect, it } from 'vitest'
import {
  directionWord,
  formatAge,
  formatContributionNote,
  formatValue,
  isFleet,
  newestAvailReadingTime,
  newestPReadingTime,
  sumFigure,
  unreportedStatusCount,
  type Fleet,
  type FleetSum,
} from './fleet'

function fleet(overrides: Partial<Fleet> = {}): Fleet {
  return {
    aggregatorLFDI: 'AGG1',
    devices: [],
    rollup: {
      deviceCount: 0,
      connected: 0,
      alarmed: 0,
      stale: 0,
      p: { sum: 0, unreported: 0, stale: 0 },
      q: { sum: 0, unreported: 0, stale: 0 },
      statWAvail: { sum: 0, unreported: 0, stale: 0 },
      statVarAvail: { sum: 0, unreported: 0, stale: 0 },
    },
    ...overrides,
  }
}

describe('sumFigure', () => {
  it('is "none" when every device is unreported, never a filled zero', () => {
    const f = fleet({ rollup: { ...fleet().rollup, deviceCount: 3, p: { sum: 0, unreported: 3, stale: 0 } } })
    const fig = sumFigure(f, f.rollup.p, null, 1000)
    expect(fig.kind).toBe('none')
  })

  it('is "none" when every device is stale, even though a stale value can sit in sum', () => {
    // A device excluded from Sum for staleness contributes nothing (see
    // addToSum in admin_fleet.go), so sum.sum is 0 here too; this proves
    // the branch is driven by the contributing count, not by sum.sum being
    // non-zero, since both zero cases must be told apart from a real 0 W.
    const f = fleet({ rollup: { ...fleet().rollup, deviceCount: 2, p: { sum: 0, unreported: 0, stale: 2 } } })
    const fig = sumFigure(f, f.rollup.p, 500, 1000)
    expect(fig.kind).toBe('none')
  })

  it('is "reporting" once at least one device contributed', () => {
    const f = fleet({ rollup: { ...fleet().rollup, deviceCount: 3, p: { sum: 1500, unreported: 1, stale: 1 } } })
    const fig = sumFigure(f, f.rollup.p, 400, 1000)
    expect(fig).toEqual({ kind: 'reporting', value: 1500, unreported: 1, stale: 1, directionKnown: false, ageSeconds: 600 })
  })

  it('floors a future-dated reading at age 0 rather than a negative age', () => {
    const f = fleet({ rollup: { ...fleet().rollup, deviceCount: 1, p: { sum: 100, unreported: 0, stale: 0 } } })
    const fig = sumFigure(f, f.rollup.p, 2000, 1000)
    expect(fig.ageSeconds).toBe(0)
  })

  it('reports no age when no device ever supplied a reading time', () => {
    const f = fleet({ rollup: { ...fleet().rollup, deviceCount: 1, p: { sum: 100, unreported: 0, stale: 0 } } })
    const fig = sumFigure(f, f.rollup.p, null, 1000)
    expect(fig.ageSeconds).toBeNull()
  })

  it('is "reporting" with value 0 for a real 0 W from a fully-reported fleet (PR 730 round 1, finding 2)', () => {
    // Kills the mutant `sum.sum !== 0 ? 'reporting' : 'none'`: that reads
    // this exact case (contributing, value 0) as 'none', which the pane
    // would render as "No devices reporting" instead of "0 W".
    const f = fleet({ rollup: { ...fleet().rollup, deviceCount: 1, p: { sum: 0, unreported: 0, stale: 0 } } })
    const fig = sumFigure(f, f.rollup.p, 900, 1000)
    expect(fig).toEqual({ kind: 'reporting', value: 0, unreported: 0, stale: 0, directionKnown: false, ageSeconds: 100 })
  })
})

describe('sumFigure directionKnown (#733)', () => {
  const base = fleet()
  const withP = (p: FleetSum) => fleet({ rollup: { ...base.rollup, deviceCount: 1, p } })

  it('is true only when the server reports directionUnknown false', () => {
    const f = withP({ sum: 100, unreported: 0, stale: 0, directionUnknown: false })
    expect(sumFigure(f, f.rollup.p, 900, 1000).directionKnown).toBe(true)
  })
  it('is false when the server reports directionUnknown true', () => {
    const f = withP({ sum: 100, unreported: 0, stale: 0, directionUnknown: true })
    expect(sumFigure(f, f.rollup.p, 900, 1000).directionKnown).toBe(false)
  })
  it('is false when the field is absent', () => {
    const f = withP({ sum: 100, unreported: 0, stale: 0 })
    expect(sumFigure(f, f.rollup.p, 900, 1000).directionKnown).toBe(false)
  })
})

describe('directionWord', () => {
  it('names export for a positive export-positive value', () => {
    expect(directionWord(500)).toBe('exporting')
  })
  it('names import for a negative export-positive value', () => {
    expect(directionWord(-500)).toBe('importing')
  })
  it('carries no direction word for exactly zero', () => {
    expect(directionWord(0)).toBe('')
  })
  it('carries no direction word for a positive value that rounds to 0 (PR 730 round 1, finding 5)', () => {
    expect(directionWord(0.4)).toBe('')
  })
  it('carries no direction word for a negative value that rounds to 0', () => {
    expect(directionWord(-0.4)).toBe('')
  })
  it('names export once the magnitude rounds up to at least 1', () => {
    expect(directionWord(0.6)).toBe('exporting')
  })
  it('names import once the negative magnitude rounds up to at least 1', () => {
    expect(directionWord(-0.6)).toBe('importing')
  })
})

describe('unreportedStatusCount', () => {
  it('counts only devices with no status field at all, not a stale one', () => {
    const f = fleet({
      devices: [
        { lfdi: 'a', measurements: {} },
        { lfdi: 'b', status: { readingTime: 1 }, measurements: {} },
        { lfdi: 'c', measurements: {} },
      ],
    })
    expect(unreportedStatusCount(f)).toBe(2)
  })

  it('is 0 for a fleet where every device reported a status', () => {
    const f = fleet({
      devices: [
        { lfdi: 'a', status: { readingTime: 1 }, measurements: {} },
        { lfdi: 'b', status: { readingTime: 2 }, measurements: {} },
      ],
    })
    expect(unreportedStatusCount(f)).toBe(0)
  })
})

describe('newestPReadingTime / newestAvailReadingTime', () => {
  it('takes the max across devices that reported the quantity, ignoring those that did not', () => {
    const f = fleet({
      devices: [
        { lfdi: 'a', measurements: { p: { value: 1, readingTime: 100 } } },
        { lfdi: 'b', measurements: { p: { value: 2, readingTime: 300 } } },
        { lfdi: 'c', measurements: {} },
      ],
    })
    expect(newestPReadingTime(f)).toBe(300)
  })

  it('is null when no device reported the quantity', () => {
    const f = fleet({ devices: [{ lfdi: 'a', measurements: {} }] })
    expect(newestPReadingTime(f)).toBeNull()
  })

  it('reads one shared readingTime per device for both availability sums', () => {
    const f = fleet({
      devices: [
        { lfdi: 'a', availability: { statWAvail: 10, statVarAvail: 5, readingTime: 700 }, measurements: {} },
      ],
    })
    expect(newestAvailReadingTime(f)).toBe(700)
  })
})

describe('formatValue', () => {
  it('formats a magnitude with a thousands separator, dropping the sign', () => {
    expect(formatValue(-12345.6)).toBe('12,346')
  })
})

describe('formatContributionNote', () => {
  it('names both unreported and stale when both are non-zero', () => {
    const note = formatContributionNote({ kind: 'reporting', value: 1, unreported: 2, stale: 1, directionKnown: false, ageSeconds: 0 })
    expect(note).toBe(' (2 unreported, 1 stale)')
  })

  it('is empty when nothing is missing', () => {
    const note = formatContributionNote({ kind: 'reporting', value: 1, unreported: 0, stale: 0, directionKnown: false, ageSeconds: 0 })
    expect(note).toBe('')
  })
})

describe('formatAge', () => {
  it('renders seconds under a minute', () => {
    expect(formatAge(45)).toBe('45s ago')
  })
  it('renders minutes under an hour', () => {
    expect(formatAge(125)).toBe('2m ago')
  })
  it('renders hours under a day', () => {
    expect(formatAge(7200)).toBe('2h ago')
  })
  it('renders days at and beyond a day', () => {
    expect(formatAge(172800)).toBe('2d ago')
  })
  it('renders unknown for a null age', () => {
    expect(formatAge(null)).toBe('unknown')
  })
})

describe('isFleet', () => {
  it('accepts a well-formed fleet', () => {
    expect(isFleet(fleet())).toBe(true)
  })

  it.each([
    ['null', null],
    ['a string', 'x'],
    ['an array', []],
    ['no rollup', { ...fleet(), rollup: undefined }],
    ['a rollup sum that is null', { ...fleet(), rollup: { ...fleet().rollup, p: null } }],
    ['a non-numeric count', { ...fleet(), rollup: { ...fleet().rollup, deviceCount: '3' } }],
    ['devices that is not an array', { ...fleet(), devices: null }],
    ['a device that is null', { ...fleet(), devices: [null] }],
    ['a device with no measurements', { ...fleet(), devices: [{ lfdi: 'a' }] }],
    ['a device with no lfdi', { ...fleet(), devices: [{ measurements: {} }] }],
    ['a non-string aggregator', { ...fleet(), aggregatorLFDI: 7 }],
  ])('rejects %s', (_name, value) => {
    expect(isFleet(value)).toBe(false)
  })
})
