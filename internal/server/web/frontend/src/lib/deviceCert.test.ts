// Proves DEVICE_TYPES cannot silently drift from internal/certs/oids.go's
// DeviceType const block: this test reads the Go source directly at test
// time, so a value renumbered or added there without a matching change
// here fails CI instead of drifting silently (issue 594's first
// acceptance criterion).
//
// tsconfig.app.json's "types" array is ["svelte", "vite/client"], with no
// "node", since the app itself never touches Node built-ins; this
// reference opts only this test file into @types/node (already a
// devDependency for vite.config.ts) rather than widening the app-wide
// config for one file's use of fs/path/process.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { DEFAULT_DEVICE_TYPE, DEVICE_TYPES } from './deviceCert'

// vitest runs with the frontend directory (internal/server/web/frontend)
// as its working directory, per this project's "test": "vitest run".
const OIDS_GO = resolve(process.cwd(), '../../../certs/oids.go')

// Maps a Go const identifier to this file's label, so the test fails both
// when a numeric value disagrees and when oids.go defines a DeviceType
// this list has never heard of.
const GO_NAME_TO_LABEL: Record<string, string> = {
  DeviceTypeGeneric: 'Generic',
  DeviceTypeMobile: 'Mobile',
  DeviceTypePostMfg: 'Post-Manufacture',
}

function parseGoDeviceTypes(source: string): Record<string, number> {
  const values: Record<string, number> = {}
  for (const match of source.matchAll(/\b(DeviceType\w+)\s+DeviceType\s*=\s*(\d+)/g)) {
    values[match[1]] = Number(match[2])
  }
  return values
}

describe('DEVICE_TYPES', () => {
  it('matches every DeviceType const in internal/certs/oids.go, by name and value', () => {
    const goTypes = parseGoDeviceTypes(readFileSync(OIDS_GO, 'utf-8'))

    expect(Object.keys(goTypes).sort()).toEqual(Object.keys(GO_NAME_TO_LABEL).sort())

    for (const [goName, value] of Object.entries(goTypes)) {
      const label = GO_NAME_TO_LABEL[goName]
      const entry = DEVICE_TYPES.find((dt) => dt.label === label)
      expect(entry, `no DEVICE_TYPES entry for ${goName} (${label})`).toBeDefined()
      expect(entry?.value).toBe(value)
    }
    expect(DEVICE_TYPES).toHaveLength(Object.keys(goTypes).length)
  })

  it('proves the parser used above can fail: a renumbered const does not match today\'s value', () => {
    const mutated = 'const (\n\tDeviceTypeGeneric DeviceType = 9\n)'
    const parsed = parseGoDeviceTypes(mutated)

    expect(parsed.DeviceTypeGeneric).toBe(9)
    expect(parsed.DeviceTypeGeneric).not.toBe(DEVICE_TYPES.find((dt) => dt.label === 'Generic')?.value)
  })

  it("defaults to Generic (1), so an operator who ignores the control sees today's behavior", () => {
    expect(DEFAULT_DEVICE_TYPE).toBe(1)
  })
})
