import { describe, expect, it } from 'vitest'
import { directionWord, isDirection } from './direction'

describe('direction', () => {
  it('accepts exactly the two words the server sends', () => {
    expect(['discharge', 'charge'].map(isDirection)).toEqual([true, true])
    expect([null, undefined, '', 'Discharge', 'sideways', 1].map(isDirection)).toEqual([false, false, false, false, false, false])
  })

  it('words a direction as sent and anything else as the caller says', () => {
    expect(directionWord('discharge', 'x')).toBe('discharge')
    expect(directionWord('charge', 'x')).toBe('charge')
    expect(directionWord(null, 'direction missing')).toBe('direction missing')
    expect(directionWord('sideways', 'direction not sent')).toBe('direction not sent')
  })
})
