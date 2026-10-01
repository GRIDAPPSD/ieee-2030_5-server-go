// The pane's TypeScript wire types are hand-written, so nothing but this
// test notices a Go json tag being renamed or added. It reads the tags out
// of the handler's own source and requires each struct's tag set to equal
// the keys the matching TypeScript interface declares.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type {
  Fleet,
  FleetDevice,
  FleetDeviceAvailability,
  FleetDeviceMeasurements,
  FleetDeviceStatus,
  FleetMeasurement,
  FleetRollup,
  FleetSum,
} from './fleet'

// vitest runs with the frontend directory as cwd, four levels below the
// repository root.
const goSource = readFileSync('../../../../internal/handler/admin_fleet.go', 'utf8')

// Each table lists every key of its interface; `satisfies Record<keyof T,
// true>` makes a key added to or removed from the interface a compile error
// here, so the table cannot drift from the type it stands for.
const wireKeys = {
  Fleet: Object.keys({ aggregatorLFDI: true, devices: true, rollup: true } satisfies Record<keyof Fleet, true>),
  FleetDevice: Object.keys({
    lfdi: true,
    edevId: true,
    href: true,
    status: true,
    availability: true,
    measurements: true,
  } satisfies Record<keyof FleetDevice, true>),
  FleetDeviceStatus: Object.keys({
    connected: true,
    operationalMode: true,
    alarmStatus: true,
    stateOfCharge: true,
    readingTime: true,
  } satisfies Record<keyof FleetDeviceStatus, true>),
  FleetDeviceAvailability: Object.keys({
    statWAvail: true,
    statVarAvail: true,
    readingTime: true,
  } satisfies Record<keyof FleetDeviceAvailability, true>),
  FleetDeviceMeasurements: Object.keys({ p: true, q: true, v: true, f: true } satisfies Record<
    keyof FleetDeviceMeasurements,
    true
  >),
  FleetMeasurement: Object.keys({
    value: true,
    readingTime: true,
    qualityFlags: true,
    directionUnknown: true,
  } satisfies Record<keyof FleetMeasurement, true>),
  FleetRollup: Object.keys({
    deviceCount: true,
    connected: true,
    alarmed: true,
    stale: true,
    p: true,
    q: true,
    statWAvail: true,
    statVarAvail: true,
  } satisfies Record<keyof FleetRollup, true>),
  FleetSum: Object.keys({
    sum: true,
    unreported: true,
    stale: true,
    directionUnknown: true,
  } satisfies Record<keyof FleetSum, true>),
}

function goTags(structName: string): string[] {
  const block = new RegExp(`type ${structName} struct \\{([\\s\\S]*?)\\n\\}`).exec(goSource)
  if (!block) throw new Error(`struct ${structName} not found in admin_fleet.go`)
  return [...block[1].matchAll(/`json:"([^",]+)/g)].map((m) => m[1])
}

// An exported field with no json tag is still sent, under its Go name, and
// goTags would not see it; every field line in the block must carry a tag.
function untaggedFields(structName: string): string[] {
  const block = new RegExp(`type ${structName} struct \\{([\\s\\S]*?)\\n\\}`).exec(goSource)
  if (!block) throw new Error(`struct ${structName} not found in admin_fleet.go`)
  return block[1]
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '' && !line.startsWith('//') && !line.includes('json:"'))
}

describe('fleet wire types against admin_fleet.go json tags', () => {
  for (const [name, keys] of Object.entries(wireKeys)) {
    it(`${name} declares exactly the keys the Go struct sends`, () => {
      const tags = goTags(name)
      expect(tags.length).toBeGreaterThan(0)
      expect([...keys].sort()).toEqual([...tags].sort())
      expect(untaggedFields(name)).toEqual([])
    })
  }
})
