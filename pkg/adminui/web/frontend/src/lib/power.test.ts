import { beforeEach, describe, expect, it } from 'vitest'
import {
  cachedPower,
  energyBars,
  energyTotals,
  forgetPower,
  formatKw,
  formatKwh,
  keepPower,
  lastPowerChoice,
  MAX_KEPT_ANSWERS,
  parsePower,
  powerBucketMs,
  powerPath,
  rememberPowerChoice,
} from './power'
import { wireAnswer } from './power.fixture'

beforeEach(() => forgetPower())

describe('powerPath', () => {
  it('asks for scope=all or one aggregator, never both', () => {
    expect(powerPath('all', 900)).toBe('/api/derms/power?window=900&scope=all')
    expect(powerPath('AB12', 3600)).toBe('/api/derms/power?window=3600&aggregatorLFDI=AB12')
  })
})

describe('parsePower', () => {
  it('keeps the fields the card reads', () => {
    const a = parsePower(wireAnswer())!
    expect(a.latestW).toBe(2000)
    expect(a.bucketSeconds).toBe(5)
    expect(a.energyChart.series.map((s) => s.name)).toEqual(['Discharged', 'Charged'])
    expect(a.devices[0].lfdi).toBe('D1')
  })

  it('reads a null latestW as null and an absent one as null, never 0', () => {
    expect(parsePower(wireAnswer({ latestW: null, latestMissing: ['D2'] }))!.latestW).toBeNull()
    const absent: Record<string, unknown> = wireAnswer()
    delete absent.latestW
    expect(parsePower(absent)!.latestW).toBeNull()
  })

  it.each([
    ['not an object', 'x'],
    ['energyChart missing', wireAnswer({ energyChart: undefined })],
    ['a point that is not a pair', wireAnswer({ energyChart: { unit: 'kWh', series: [{ name: 'a', points: [[1]] }] } })],
    ['latestW a string', wireAnswer({ latestW: '2000' })],
    ['notReporting not strings', wireAnswer({ notReporting: [1] })],
    ['a device without flags', wireAnswer({ devices: [{ lfdi: 'D1' }] })],
  ])('refuses a reply with %s whole', (_why, value) => {
    expect(parsePower(value)).toBeNull()
  })
})

describe('the kept answer', () => {
  it('is kept per scope and window and forgotten together', () => {
    const answer = parsePower(wireAnswer())!
    keepPower('all', 900, { answer, at: 5 })
    expect(cachedPower('all', 900)).toEqual({ answer, at: 5 })
    expect(cachedPower('all', 3600)).toBeUndefined()
    expect(cachedPower('AGG1', 900)).toBeUndefined()
    rememberPowerChoice('AGG1', 3600)
    expect(lastPowerChoice()).toEqual({ scope: 'AGG1', window: 3600 })
    forgetPower()
    expect(cachedPower('all', 900)).toBeUndefined()
    expect(lastPowerChoice()).toEqual({ scope: 'all', window: 900 })
  })
})

describe('the kept answers bound', () => {
  it('drops the oldest answer past the cap and keeps the newest', () => {
    const answer = parsePower(wireAnswer())!
    for (let i = 0; i < MAX_KEPT_ANSWERS + 1; i++) keepPower(`AGG${i}`, 900, { answer, at: i })
    expect(cachedPower('AGG0', 900)).toBeUndefined()
    expect(cachedPower('AGG1', 900)?.at).toBe(1)
    expect(cachedPower(`AGG${MAX_KEPT_ANSWERS}`, 900)?.at).toBe(MAX_KEPT_ANSWERS)
  })

  it('counts a re-kept answer as newest, not as a second entry', () => {
    const answer = parsePower(wireAnswer())!
    keepPower('AGG0', 900, { answer, at: 0 })
    for (let i = 1; i < MAX_KEPT_ANSWERS; i++) keepPower(`AGG${i}`, 900, { answer, at: i })
    keepPower('AGG0', 900, { answer, at: 99 })
    keepPower('NEW', 900, { answer, at: 100 })
    expect(cachedPower('AGG0', 900)?.at).toBe(99)
    expect(cachedPower('AGG1', 900)).toBeUndefined()
  })
})

describe('formatting', () => {
  it('shows watts as kW to one decimal, sign kept', () => {
    expect(formatKw(2000)).toBe('2.0 kW')
    expect(formatKw(-1250)).toBe('-1.3 kW')
  })

  it('polls once per bucket the server uses for the window', () => {
    expect([900, 3600, 21600, 86400].map(powerBucketMs)).toEqual([5000, 5000, 30000, 120000])
  })
})

describe('energyTotals', () => {
  it('sums each series over the window, discharge positive and charge negative', () => {
    const a = parsePower(
      wireAnswer({
        energyChart: {
          unit: 'kWh',
          series: [
            { name: 'Discharged', points: [[1000, 0.5], [2000, 0.25]] },
            { name: 'Charged', points: [[1000, -0.125], [2000, 0]] },
          ],
        },
      }),
    )!
    expect(energyTotals(a.energyChart)).toEqual({ discharged: 0.75, charged: -0.125 })
    expect(formatKwh(0.75)).toBe('0.7500 kWh')
  })

  it('reads a missing series as zero', () => {
    expect(energyTotals({ unit: 'kWh', series: [] })).toEqual({ discharged: 0, charged: 0 })
  })
})

describe('energyBars', () => {
  it('types each series a bar, Charged as charge and the rest as discharge, keeping names and points', () => {
    const chart = {
      unit: 'kWh',
      series: [
        { name: 'Discharged', points: [[1000, 0.5]] as [number, number][] },
        { name: 'Charged', points: [[1000, -0.25]] as [number, number][] },
      ],
    }
    expect(energyBars(chart).series).toEqual([
      { name: 'Discharged', points: [[1000, 0.5]], type: 'bar', direction: 'discharge' },
      { name: 'Charged', points: [[1000, -0.25]], type: 'bar', direction: 'charge' },
    ])
    expect(energyBars(chart).unit).toBe('kWh')
    expect(chart.series[0]).not.toHaveProperty('type')
  })
})
