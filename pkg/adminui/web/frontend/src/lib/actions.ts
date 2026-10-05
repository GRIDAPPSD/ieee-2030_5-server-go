// Panel actions: the schema the server serves, the checks it applies, and
// the wording of its answers (pkg/sep2admin/action.go,
// internal/adminplane/panel_actions.go). Every check here is a copy of a
// server check, made so a refused value never leaves the page; the server
// stays the authority.

export interface ActionChoice {
  id: string
  label: string
}

export interface ActionField {
  name: string
  label: string
  kind: 'choice' | 'integer' | 'boolean' | 'toggle' | 'text'
  min?: number
  max?: number
  maxLen?: number
  choices?: ActionChoice[]
  // Set when the schema holds a field this form cannot send exactly; the
  // field shows the reason and the rest of the panel stays usable.
  problem?: string
}

export interface ActionSpec {
  id: string
  label: string
  fields: ActionField[]
}

// A field's value as the form holds it. An integer is the typed text, a
// toggle is undefined until the operator or the server has set it.
export type FieldValue = string | boolean | undefined

export const MAX_ACTION_BODY_BYTES = 16 << 10

// Above the plane's own action timeout (5 s unless the embedder sets
// another), so the server's answer normally arrives first.
export const ACTION_TIMEOUT_MS = 60_000

// The 504 the server sends when it refused to start the action because an
// earlier request still held the panel. The body carries no code, so the
// sentence is matched; a reworded sentence falls back to "may have run".
const BUSY_REFUSAL = 'panel is still answering an earlier request'

export function actionURL(panelId: string, actionId?: string): string {
  const base = `/api/ui/panels/${encodeURIComponent(panelId)}/actions`
  return actionId === undefined ? base : `${base}/${encodeURIComponent(actionId)}`
}

const KINDS = new Set(['choice', 'integer', 'boolean', 'toggle', 'text'])

function parseField(v: unknown): ActionField | null {
  if (typeof v !== 'object' || v === null) return null
  const f = v as Record<string, unknown>
  if (typeof f.name !== 'string' || typeof f.label !== 'string' || typeof f.kind !== 'string' || !KINDS.has(f.kind)) {
    return null
  }
  const out: ActionField = { name: f.name, label: f.label, kind: f.kind as ActionField['kind'] }
  if (out.kind === 'integer') {
    if (typeof f.min !== 'number' || typeof f.max !== 'number') return null
    if (Number.isSafeInteger(f.min) && Number.isSafeInteger(f.max)) {
      out.min = f.min
      out.max = f.max
    } else {
      out.problem = 'This number range is too wide for this form, so this action cannot be run here.'
    }
  }
  if (out.kind === 'text') {
    if (!Number.isSafeInteger(f.maxLen) || (f.maxLen as number) < 1) return null
    out.maxLen = f.maxLen as number
  }
  if (out.kind === 'choice') {
    if (!Array.isArray(f.choices)) return null
    const choices: ActionChoice[] = []
    for (const c of f.choices) {
      if (typeof c !== 'object' || c === null) return null
      const { id, label } = c as Record<string, unknown>
      if (typeof id !== 'string' || typeof label !== 'string') return null
      choices.push({ id, label })
    }
    out.choices = choices
  }
  return out
}

// Null when the reply is not the schema this build understands, so the
// caller says so instead of rendering a half-read form.
export function parseActions(data: unknown): ActionSpec[] | null {
  if (typeof data !== 'object' || data === null) return null
  const list = (data as Record<string, unknown>).actions
  if (!Array.isArray(list)) return null
  const out: ActionSpec[] = []
  for (const a of list) {
    if (typeof a !== 'object' || a === null) return null
    const { id, label, fields } = a as Record<string, unknown>
    if (typeof id !== 'string' || typeof label !== 'string' || !Array.isArray(fields)) return null
    const parsed: ActionField[] = []
    for (const f of fields) {
      const pf = parseField(f)
      if (pf === null) return null
      parsed.push(pf)
    }
    out.push({ id, label, fields: parsed })
  }
  return out
}

// The server's validActionText: no control character but tab, newline and
// carriage return, none of the C1 set, and none of the bidirectional
// embedding, override and isolate controls.
function hasRefusedChar(s: string): boolean {
  for (const ch of s) {
    const r = ch.codePointAt(0) as number
    if ((r < 0x20 && r !== 0x0a && r !== 0x0d && r !== 0x09) || (r >= 0x7f && r <= 0x9f)) return true
    if ((r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)) return true
  }
  return false
}

const INTEGER_LITERAL = /^-?(0|[1-9][0-9]*)$/

// The message to show for a value the server would refuse, or null.
export function validateField(f: ActionField, value: FieldValue): string | null {
  if (f.problem !== undefined) return f.problem
  switch (f.kind) {
    case 'integer': {
      const text = typeof value === 'string' ? value.trim() : ''
      if (!INTEGER_LITERAL.test(text)) return 'Enter a whole number.'
      const n = Number(text)
      if (!Number.isSafeInteger(n)) return 'This number is too large for this form.'
      if (n < (f.min as number) || n > (f.max as number)) return `Enter a number from ${f.min} to ${f.max}.`
      return null
    }
    case 'text': {
      const text = typeof value === 'string' ? value : ''
      const bytes = new TextEncoder().encode(text).length
      if (bytes < 1) return 'Enter some text.'
      if (bytes > (f.maxLen as number)) return `The text can be at most ${f.maxLen} bytes; this is ${bytes}.`
      if (hasRefusedChar(text)) return 'The text holds a control character this action does not accept.'
      return null
    }
    case 'choice': {
      if (typeof value !== 'string' || value === '') return 'Choose one.'
      return (f.choices ?? []).some((c) => c.id === value) ? null : 'Choose one of the listed items.'
    }
    case 'toggle':
      return typeof value === 'boolean' ? null : 'Set this switch on or off.'
    case 'boolean':
      return typeof value === 'boolean' ? null : 'Check or clear this box.'
  }
}

// Field name to message for every field the server would refuse; empty
// when the whole form is acceptable, including the body cap.
export function validateForm(fields: ActionField[], values: Record<string, FieldValue>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of fields) {
    const problem = validateField(f, values[f.name])
    if (problem !== null) out[f.name] = problem
  }
  return out
}

export function buildBody(fields: ActionField[], values: Record<string, FieldValue>): Record<string, string | number | boolean> {
  const body: Record<string, string | number | boolean> = {}
  for (const f of fields) {
    const v = values[f.name]
    body[f.name] = f.kind === 'integer' ? Number((v as string).trim()) : (v as string | boolean)
  }
  return body
}

// The encoded body is what the server caps, so a text full of quotes or
// newlines reaches the cap before its own length does.
export function bodyTooLarge(body: unknown): boolean {
  return new TextEncoder().encode(JSON.stringify(body)).length > MAX_ACTION_BODY_BYTES
}

export type Outcome =
  | { kind: 'ok'; text: string }
  | { kind: 'refused'; text: string; field?: string }
  | { kind: 'error'; text: string }
  // The server did not say whether the action ran. Never retried by the page.
  | { kind: 'unknown'; text: string }

function messageOf(data: unknown): string {
  if (typeof data === 'object' && data !== null) {
    const m = (data as Record<string, unknown>).message
    if (typeof m === 'string' && m !== '') return m
  }
  return 'Done.'
}

// Turns an api.ts result into what the form shows. A 400 that names a
// declared field is shown beside that field; a 504 shows the server's own
// sentence, which says the action may still complete.
export function describeOutcome(
  res: { ok: true; data: unknown } | { ok: false; error: string; status: number; body?: unknown },
  fieldNames: string[],
): Outcome {
  if (res.ok) return { kind: 'ok', text: messageOf(res.data) }
  switch (res.status) {
    case 400: {
      const field = res.body && typeof res.body === 'object' ? (res.body as Record<string, unknown>).field : undefined
      if (typeof field === 'string' && fieldNames.includes(field)) {
        return { kind: 'refused', text: `The server refused this value: ${res.error}.`, field }
      }
      return { kind: 'refused', text: `The server refused this submission: ${res.error}.` }
    }
    case 422:
      return { kind: 'refused', text: res.error }
    case 401:
      return { kind: 'error', text: 'Admin session required. Sign in again.' }
    case 403:
      return { kind: 'error', text: `The server refused this request: ${res.error}.` }
    case 404:
      return { kind: 'error', text: 'This action is not available. Panel actions may be turned off.' }
    case 413:
      return { kind: 'error', text: 'The submission is too large for the server.' }
    case 415:
      return { kind: 'error', text: `The server did not accept the request format: ${res.error}.` }
    case 429:
      return { kind: 'error', text: 'Too many actions in a short time. Wait a few seconds, then try again.' }
    case 503:
      // A canceled request may have started the action.
      return { kind: 'unknown', text: `The server is shutting down or canceled the request: ${res.error}` }
    case 504:
      if (res.error === BUSY_REFUSAL) {
        return { kind: 'error', text: `The panel is still answering an earlier request. The action did not run; try again in a moment.` }
      }
      return { kind: 'unknown', text: res.error }
    case 0:
      return {
        kind: 'unknown',
        text: `The request did not finish (${res.error}). The action may still complete, so check before trying again.`,
      }
    default:
      return { kind: 'error', text: `The action failed: ${res.error}` }
  }
}
