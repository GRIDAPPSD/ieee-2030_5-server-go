import { describe, expect, it } from 'vitest'
import { STREAM_LOG_CAP, appendLine, parseStreamEvent, streamURL, validateStreamParam, type StreamLine } from './stream'

const info = { maxLen: 5, charset: 'abc-' }

describe('validateStreamParam', () => {
  it.each([
    ['abc', null],
    ['a-b-c', null],
    ['', 'Enter a value to stream.'],
    ['abcabc', 'The value can be at most 5 characters.'],
    ['abd', 'The value holds a character this panel does not accept.'],
    ['ab c', 'The value holds a character this panel does not accept.'],
  ])('value %j gives %j', (value, want) => {
    expect(validateStreamParam(value, info)).toBe(want)
  })

  it('counts bytes, as the server does, not characters', () => {
    const wide = { maxLen: 2, charset: 'a\u00e9' }
    expect(validateStreamParam('a\u00e9', wide)).toBe('The value can be at most 2 characters.')
  })
})

const line = (key: number): StreamLine => ({ key, kind: 'message', text: `m${key}`, time: 't' })

describe('appendLine', () => {
  it('keeps exactly the newest cap lines, oldest dropped first', () => {
    let lines: StreamLine[] = []
    for (let i = 0; i < STREAM_LOG_CAP + 25; i++) lines = appendLine(lines, line(i))
    expect(lines).toHaveLength(1000)
    expect(lines[0].key).toBe(25)
    expect(lines[999].key).toBe(1024)
  })

  it('does not mutate the list it was given', () => {
    const before = [line(1)]
    appendLine(before, line(2))
    expect(before).toHaveLength(1)
  })
})

describe('streamURL', () => {
  it('sends the one param, encoded, on the encoded panel id', () => {
    expect(streamURL('a b', 'x&y=z')).toBe('/api/ui/panels/a%20b/stream?param=x%26y%3Dz')
  })
})

describe('parseStreamEvent', () => {
  it('reads a message and a status event', () => {
    expect(parseStreamEvent('{"id":"3","time":"2026-10-05T00:00:00Z","kind":"message","text":"hi"}')).toEqual({
      time: '2026-10-05T00:00:00Z',
      kind: 'message',
      text: 'hi',
    })
    expect(parseStreamEvent('{"time":"t","kind":"status","text":"over"}')?.kind).toBe('status')
  })

  it.each(['not json', '{"time":"t","kind":"other","text":"x"}', '{"time":"t","kind":"message"}', '[]', 'null'])(
    'refuses %j',
    (data) => {
      expect(parseStreamEvent(data)).toBeNull()
    },
  )
})
