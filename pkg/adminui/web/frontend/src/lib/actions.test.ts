import { describe, expect, it } from 'vitest'
import {
  MAX_ACTION_BODY_BYTES,
  actionURL,
  bodyTooLarge,
  buildBody,
  describeOutcome,
  parseActions,
  validateField,
  validateForm,
  type ActionField,
} from './actions'

const integer: ActionField = { name: 'n', label: 'N', kind: 'integer', min: -2, max: 10 }
const text: ActionField = { name: 't', label: 'T', kind: 'text', maxLen: 6 }
const choice: ActionField = { name: 'c', label: 'C', kind: 'choice', choices: [{ id: 'a', label: 'A' }] }
const toggle: ActionField = { name: 'k', label: 'K', kind: 'toggle' }

describe('validateField', () => {
  it('accepts an integer at both inclusive bounds and refuses one past each', () => {
    expect(validateField(integer, '-2')).toBeNull()
    expect(validateField(integer, '10')).toBeNull()
    expect(validateField(integer, '-3')).toBe('Enter a number from -2 to 10.')
    expect(validateField(integer, '11')).toBe('Enter a number from -2 to 10.')
  })

  it.each(['1.0', '1e2', '', ' ', '+3', '012', 'abc', '0x10'])('refuses the integer literal %j the server would refuse', (v) => {
    expect(validateField(integer, v)).toBe('Enter a whole number.')
  })

  it('refuses an integer beyond the exactly representable range rather than sending a rounded one', () => {
    const wide: ActionField = { ...integer, min: 0, max: Number.MAX_SAFE_INTEGER }
    expect(validateField(wide, '9007199254740993')).toBe('This number is too large for this form.')
  })

  it('counts text in UTF-8 bytes, not characters', () => {
    expect(validateField(text, 'abcdef')).toBeNull()
    expect(validateField(text, 'abcdefg')).toBe('The text can be at most 6 bytes; this is 7.')
    // Two characters, six bytes: accepted. Three, nine bytes: refused.
    expect(validateField(text, '\u20ac\u20ac')).toBeNull()
    expect(validateField(text, '\u20ac\u20ac\u20ac')).toBe('The text can be at most 6 bytes; this is 9.')
  })

  it('refuses empty text and the control characters the server refuses, but not tab or newline', () => {
    expect(validateField(text, '')).toBe('Enter some text.')
    const refused = ['\u0000', '\u001f', '\u007f', '\u0080', '\u009f', '\u202a', '\u202b', '\u202c', '\u202d', '\u202e', '\u2066', '\u2067', '\u2068', '\u2069']
    for (const bad of refused.map((c) => 'a' + c)) {
      expect(validateField(text, bad)).toBe('The text holds a control character this action does not accept.')
    }
    expect(validateField(text, 'a\tb\r\n')).toBeNull()
    // The characters just outside each refused range are accepted.
    for (const ok of ['\u0020', '\u00a0', '\u2029', '\u202f', '\u2065', '\u206a']) expect(validateField(text, 'a' + ok)).toBeNull()
  })

  it('accepts only a choice listed in the schema', () => {
    expect(validateField(choice, 'a')).toBeNull()
    expect(validateField(choice, 'zz')).toBe('Choose one of the listed items.')
    expect(validateField(choice, '')).toBe('Choose one.')
  })

  it('requires a toggle to have been set, since a default would be a guess', () => {
    expect(validateField(toggle, undefined)).toBe('Set this switch on or off.')
    expect(validateField(toggle, false)).toBeNull()
    expect(validateField(toggle, true)).toBeNull()
  })
})

describe('buildBody', () => {
  it('sends integers as JSON numbers and the rest as held', () => {
    const body = buildBody([integer, text, toggle, choice], { n: ' 7 ', t: 'hi', k: false, c: 'a' })
    expect(JSON.stringify(body)).toBe('{"n":7,"t":"hi","k":false,"c":"a"}')
    expect(Object.keys(validateForm([integer, text], { n: '7', t: 'x' }))).toEqual([])
  })

  it('judges the body cap on the encoded bytes, where quotes and newlines grow', () => {
    const body = { t: 'x'.repeat(MAX_ACTION_BODY_BYTES - 8) }
    expect(bodyTooLarge(body)).toBe(false)
    expect(bodyTooLarge({ t: '"'.repeat(MAX_ACTION_BODY_BYTES / 2) })).toBe(true)
  })
})

describe('parseActions', () => {
  const wire = {
    actions: [
      {
        id: 'go',
        label: 'Go',
        fields: [
          { name: 'n', label: 'N', kind: 'integer', min: 1, max: 3 },
          { name: 'c', label: 'C', kind: 'choice', choices: [{ id: 'a', label: 'A' }] },
          { name: 't', label: 'T', kind: 'text', maxLen: 10 },
          { name: 'k', label: 'K', kind: 'toggle' },
        ],
      },
    ],
  }

  it('reads every field kind with its bounds', () => {
    const got = parseActions(wire)
    expect(got?.[0].fields.map((f) => [f.kind, f.min, f.max, f.maxLen, f.choices?.length])).toEqual([
      ['integer', 1, 3, undefined, undefined],
      ['choice', undefined, undefined, undefined, 1],
      ['text', undefined, undefined, 10, undefined],
      ['toggle', undefined, undefined, undefined, undefined],
    ])
  })

  it('keeps an int64-wide integer field, with a reason, instead of refusing the whole schema', () => {
    const got = parseActions({
      actions: [{ id: 'g', label: 'G', fields: [{ name: 'n', label: 'N', kind: 'integer', min: 0, max: 9223372036854775807 }] }],
    })
    expect(got?.[0].fields[0].problem).toMatch(/too wide/)
    expect(validateField(got?.[0].fields[0] as ActionField, '5')).toMatch(/too wide/)
  })

  it('returns null for an integer without bounds, an unknown kind, or a non-object', () => {
    expect(parseActions({ actions: [{ id: 'g', label: 'G', fields: [{ name: 'n', label: 'N', kind: 'integer' }] }] })).toBeNull()
    expect(parseActions({ actions: [{ id: 'g', label: 'G', fields: [{ name: 'n', label: 'N', kind: 'color' }] }] })).toBeNull()
    expect(parseActions(null)).toBeNull()
    expect(parseActions({})).toBeNull()
  })
})

describe('describeOutcome', () => {
  const names = ['n', 'c']
  const fail = (status: number, error: string, body?: unknown) => ({ ok: false as const, status, error, body })

  it('shows the server message on success', () => {
    expect(describeOutcome({ ok: true, data: { ok: true, message: 'sent' } }, names)).toEqual({ kind: 'ok', text: 'sent' })
  })

  it('ties a 400 to the declared field it names, and only a declared one', () => {
    const o = describeOutcome(fail(400, 'invalid action values', { error: 'invalid action values', field: 'n' }), names)
    expect(o).toEqual({ kind: 'refused', text: 'The server refused this value: invalid action values.', field: 'n' })
    const stray = describeOutcome(fail(400, 'invalid action values', { field: '<b>x</b>' }), names)
    expect(stray.kind === 'refused' && stray.field).toBeFalsy()
  })

  it('shows a 422 refusal reason as the server worded it', () => {
    expect(describeOutcome(fail(422, 'publishing is off'), names)).toEqual({ kind: 'refused', text: 'publishing is off' })
  })

  it('gives 429 and 503 their own wording', () => {
    expect(describeOutcome(fail(429, 'too many panel actions'), names).text).toMatch(/Wait a few seconds/)
    expect(describeOutcome(fail(503, 'plane is shutting down'), names).text).toMatch(/shutting down.*plane is shutting down/)
  })

  it('keeps the server sentence on a 504 and marks the outcome unknown', () => {
    const s = 'action did not answer in time; it may still complete, so do not retry blindly'
    expect(describeOutcome(fail(504, s), names)).toEqual({ kind: 'unknown', text: s })
  })

  it('says a busy 504 means the action did not run, and keeps a timeout 504 unknown', () => {
    const busy = describeOutcome(fail(504, 'panel is still answering an earlier request'), names)
    expect(busy.kind).toBe('error')
    expect(busy.text).toMatch(/did not run/)
    expect(describeOutcome(fail(504, 'something else'), names).kind).toBe('unknown')
  })

  it('treats a cancel 503 as unknown, since the action may still complete', () => {
    expect(describeOutcome(fail(503, 'request canceled'), names).kind).toBe('unknown')
  })

  it('treats a request that never finished as unknown, not as failed', () => {
    const o = describeOutcome(fail(0, 'request timed out'), names)
    expect(o.kind).toBe('unknown')
    expect(o.text).toMatch(/may still complete/)
  })

  it('builds the action URL with encoded segments', () => {
    expect(actionURL('a b', 'go')).toBe('/api/ui/panels/a%20b/actions/go')
    expect(actionURL('p')).toBe('/api/ui/panels/p/actions')
  })
})
