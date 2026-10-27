import { beforeEach, describe, expect, it } from 'vitest'
import {
  cachedPower,
  aggregatorBars,
  aggregatorLabels,
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
    expect(formatKwh(0.75)).toBe('0.750 kWh')
    expect(formatKwh(0.168)).toBe('0.168 kWh')
    expect(formatKwh(0.0004)).toBe('0.000 kWh')
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

describe('aggregatorBars', () => {
  const entry = (key: string, name: string, charged: [number, number | null][] = [[10, 0.2]]) => ({
    key,
    name,
    latestW: null,
    latestMissing: [],
    discharged: [[10, 0.5], [15, null]] as [number, number | null][],
    charged,
  })
  const labelOf = (l: string) => `dev-${l}`

  it('splits each aggregator into discharge above zero and charge below, dropping empty buckets', () => {
    const body = aggregatorBars([entry('A', 'wire-name'), entry('direct', 'Direct')], 'kWh', labelOf)
    expect(body.unit).toBe('kWh')
    expect(body.series).toEqual([
      { id: 'A:discharge', name: 'dev-A discharge', hint: 'A', points: [[10000, 0.5]], type: 'bar', direction: 'discharge', colorIndex: 0 },
      { id: 'A:charge', name: 'dev-A charge', hint: 'A', points: [[10000, -0.2]], type: 'bar', direction: 'charge', colorIndex: 0 },
      { id: 'direct:discharge', name: 'Direct discharge', points: [[10000, 0.5]], type: 'bar', direction: 'discharge', colorIndex: 4 },
      { id: 'direct:charge', name: 'Direct charge', points: [[10000, -0.2]], type: 'bar', direction: 'charge', colorIndex: 4 },
    ])
  })

  it('draws charge below zero whichever sign the wire used', () => {
    const body = aggregatorBars([entry('A', 'n', [[10, 0.2], [20, -0.3]])], 'kWh', labelOf)
    expect(body.series[1].points).toEqual([[10000, -0.2], [20000, -0.3]])
  })

  it('labels by device label, not the wire name, and keeps same-label aggregators apart', () => {
    const lfdi1 = 'AABBCCDD11223344'
    const lfdi2 = 'EEFF001122334455'
    const body = aggregatorBars([entry(lfdi1, lfdi1), entry(lfdi2, lfdi2)], 'kWh', () => 'Roof')
    expect(body.series.map((x) => x.name)).toEqual([
      'Roof (AABBCCDD) discharge',
      'Roof (AABBCCDD) charge',
      'Roof (EEFF0011) discharge',
      'Roof (EEFF0011) charge',
    ])
    expect(new Set(body.series.map((x) => x.id)).size).toBe(4)
    expect(aggregatorLabels([entry('direct', 'x')], () => 'never')).toEqual(['Direct'])
  })
})

describe('parsePower per-aggregator energy', () => {
  it('reads an absent field as null and refuses a malformed one', () => {
    expect(parsePower(wireAnswer())?.aggregatorEnergy).toBeNull()
    expect(parsePower(wireAnswer({ aggregatorEnergy: [{ key: 'A' }] }))).toBeNull()
    const ok = { key: 'A', name: 'N', latestW: null, latestMissing: [], discharged: [[1, null]], charged: [] }
    expect(parsePower(wireAnswer({ aggregatorEnergy: [ok] }))?.aggregatorEnergy).toEqual([ok])
  })
})
