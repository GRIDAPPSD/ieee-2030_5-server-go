import { describe, expect, it } from 'vitest'
import { originMarker } from './dercontrol'

describe('originMarker (#1155)', () => {
  it('marks only a derms origin, with the direction the server names', () => {
    expect(originMarker('derms', 'charge')).toBe('from DERMS, charge')
    expect(originMarker('derms', 'discharge')).toBe('from DERMS, discharge')
    expect(originMarker('derms', null)).toBe('from DERMS')
    expect(originMarker('admin', 'charge')).toBe('')
    expect(originMarker(undefined, 'charge')).toBe('')
  })
})
