// The DER control list's wire types are hand-written; this test requires
// each one's keys to equal the json tags of the Go struct it mirrors.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { DERControlDelivery, DERControlListItem, DERControlView } from './dercontrol'

// vitest runs with the frontend directory as cwd, four levels below the
// repository root.
const handlerDir = '../../../../internal/handler/'

const wire = {
  DERControlDelivery: {
    file: 'admin_dercontrol_delivery.go',
    keys: Object.keys({
      windowStart: true,
      windowEnd: true,
      deliveredWh: true,
      averageW: true,
      coveredSeconds: true,
      readings: true,
      directionUnknown: true,
      concurrentMirrors: true,
      excludedReadings: true,
      deviceLFDI: true,
      newestReadingTime: true,
    } satisfies Record<keyof DERControlDelivery, true>),
  },
  // Only the list item's own fields; the embedded DERControlView is untagged.
  DERControlListItem: {
    file: 'admin_dercontrol.go',
    keys: Object.keys({ responses: true, delivery: true } satisfies Record<Exclude<keyof DERControlListItem, keyof DERControlView>, true>),
  },
}

function structBody(file: string, name: string): string {
  const src = readFileSync(handlerDir + file, 'utf8')
  const block = new RegExp(`type ${name} struct \\{([\\s\\S]*?)\\n\\}`).exec(src)
  if (!block) throw new Error(`struct ${name} not found in ${file}`)
  return block[1]
}

describe('DER control wire types against the Go json tags', () => {
  for (const [name, { file, keys }] of Object.entries(wire)) {
    it(`${name} declares exactly the keys the Go struct sends`, () => {
      const body = structBody(file, name)
      const tags = [...body.matchAll(/`json:"([^",]+)/g)].map((m) => m[1])
      expect(tags.length).toBeGreaterThan(0)
      expect([...keys].sort()).toEqual([...tags].sort())
      const untagged = body
        .split('\n')
        .map((l) => l.trim())
        .filter((l) => l !== '' && !l.startsWith('//') && !l.includes('json:"') && l !== 'DERControlView')
      expect(untagged).toEqual([])
    })
  }
})
