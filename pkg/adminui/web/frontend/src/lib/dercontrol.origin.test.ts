import { describe, expect, it } from 'vitest'
import { originMarker, targetDirection } from './dercontrol'

describe('originMarker and targetDirection (#1155)', () => {
  it('reads the sign of opModTargetW as discharge positive', () => {
    expect(targetDirection({ opModTargetW: { value: 5, multiplier: 0 } })).toBe('discharge')
    expect(targetDirection({ opModTargetW: { value: -5, multiplier: 0 } })).toBe('charge')
    expect(targetDirection({ opModTargetW: { value: 0, multiplier: 0 } })).toBeNull()
    expect(targetDirection({ opModConnect: true })).toBeNull()
    expect(targetDirection(undefined)).toBeNull()
  })

  it('marks only a derms origin', () => {
    const f = { opModTargetW: { value: -5, multiplier: 0 } }
    expect(originMarker('derms', f)).toBe('from DERMS, charge')
    expect(originMarker('derms', { opModConnect: true })).toBe('from DERMS')
    expect(originMarker('admin', f)).toBe('')
    expect(originMarker(undefined, f)).toBe('')
  })
})
