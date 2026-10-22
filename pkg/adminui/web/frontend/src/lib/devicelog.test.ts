import { describe, expect, it } from 'vitest'
import { mergeNewer, mergeOlder, parsePage } from './devicelog'

const e = (cursor: string) => ({ cursor, source: '', line: {} })

describe('parsePage', () => {
  it('rejects a body without an entries array and an entry without a cursor', () => {
    expect(parsePage(null)).toBeNull()
    expect(parsePage({ lfdi: 'x' })).toBeNull()
    expect(parsePage({ entries: [{ line: {} }] })).toBeNull()
  })

  it('turns an empty cursor into null and keeps the entry fields', () => {
    const p = parsePage({
      entries: [{ cursor: 'a', source: 'unregistered', line: { kind: 'request' } }],
      nextBefore: '',
      nextAfter: 'a',
      dropped: 3,
    })
    expect(p).toEqual({ entries: [{ cursor: 'a', source: 'unregistered', line: { kind: 'request' } }], nextBefore: null, nextAfter: 'a', dropped: 3 })
  })
})

describe('merging', () => {
  it('puts newer lines in front and older behind, skipping held cursors', () => {
    expect(mergeNewer([e('b'), e('a')], [e('c'), e('b')]).map((x) => x.cursor)).toEqual(['c', 'b', 'a'])
    expect(mergeOlder([e('c'), e('b')], [e('b'), e('a')]).map((x) => x.cursor)).toEqual(['c', 'b', 'a'])
  })
})
