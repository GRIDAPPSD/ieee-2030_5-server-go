// The wire types are hand-written, so this test reads the json tags out of
// the handler source and requires each struct's tag set to equal the keys
// of the matching TypeScript interface.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { CommittedControl, CommittedGrant, Commitments } from './commitments'
import type { Interval } from './flowreservation'

// vitest runs with the frontend directory as cwd, four levels below the
// repository root.
const commitmentsSource = readFileSync('../../../../internal/handler/admin_commitments.go', 'utf8')
const intervalSource = readFileSync('../../../../internal/handler/admin_flowreservation.go', 'utf8')

const cases: { name: string; source: string; keys: string[] }[] = [
  {
    name: 'CommitmentsView',
    source: commitmentsSource,
    keys: Object.keys({ aggregatorLFDI: true, now: true, grants: true, plainControls: true } satisfies Record<keyof Commitments, true>),
  },
  {
    name: 'CommittedGrantView',
    source: commitmentsSource,
    keys: Object.keys({
      mRID: true,
      edevId: true,
      frqId: true,
      window: true,
      direction: true,
      powerW: true,
      energyWh: true,
      energyRemainingWh: true,
    } satisfies Record<keyof CommittedGrant, true>),
  },
  {
    name: 'CommittedControlView',
    source: commitmentsSource,
    keys: Object.keys({ mRID: true, edevId: true, window: true, targetW: true } satisfies Record<keyof CommittedControl, true>),
  },
  {
    name: 'frInterval',
    source: intervalSource,
    keys: Object.keys({ start: true, duration: true } satisfies Record<keyof Interval, true>),
  },
]

function block(source: string, name: string): string {
  const m = new RegExp(`type ${name} struct \\{([\\s\\S]*?)\\n\\}`).exec(source)
  if (!m) throw new Error(`struct ${name} not found`)
  return m[1]
}

describe('commitments wire types against Go json tags', () => {
  for (const { name, source, keys } of cases) {
    it(`${name} declares exactly the keys the Go struct sends`, () => {
      const body = block(source, name)
      const tags = [...body.matchAll(/`json:"([^",]+)/g)].map((m) => m[1])
      expect(tags.length).toBeGreaterThan(0)
      expect([...keys].sort()).toEqual([...tags].sort())
      const untagged = body
        .split('\n')
        .map((l) => l.trim())
        .filter((l) => l !== '' && !l.startsWith('//') && !l.includes('json:"'))
      expect(untagged).toEqual([])
    })
  }
})
