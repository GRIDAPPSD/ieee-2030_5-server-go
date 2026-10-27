import { describe, expect, it } from 'vitest'
import { nodeColor } from './fsa'

describe('nodeColor', () => {
  it('maps each topology level to a design token', () => {
    expect(['SY', 'FD', 'SP', 'DEV', 'other'].map(nodeColor)).toEqual([
      'var(--chart-1)',
      'var(--chart-2)',
      'var(--chart-3)',
      'var(--text)',
      'var(--text)',
    ])
  })
})
