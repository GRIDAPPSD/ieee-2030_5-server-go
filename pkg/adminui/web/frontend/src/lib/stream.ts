import type { PanelStreamInfo } from './descriptor'

// The in-browser log keeps the newest lines only, so a feed that runs for
// days cannot grow the page without bound.
export const STREAM_LOG_CAP = 1000

export interface StreamLine {
  key: number
  kind: 'message' | 'status'
  text: string
  time: string
}

// The server's StreamParam.Validate, applied before the request so a
// refused value never leaves the page: 1 to maxLen bytes, every one of
// them in the advertised charset. Returns the message to show, or null.
export function validateStreamParam(value: string, info: PanelStreamInfo): string | null {
  if (value.length === 0) return 'Enter a value to stream.'
  if (new TextEncoder().encode(value).length > info.maxLen) {
    return `The value can be at most ${info.maxLen} characters.`
  }
  for (const ch of value) {
    if (!info.charset.includes(ch)) return 'The value holds a character this panel does not accept.'
  }
  return null
}

export function appendLine(lines: StreamLine[], line: StreamLine, cap: number = STREAM_LOG_CAP): StreamLine[] {
  const next = [...lines, line]
  return next.length > cap ? next.slice(next.length - cap) : next
}

export function streamURL(id: string, param: string): string {
  return `/api/ui/panels/${encodeURIComponent(id)}/stream?${new URLSearchParams({ param }).toString()}`
}

export interface StreamEventData {
  time: string
  kind: 'message' | 'status'
  text: string
}

// Null for data that is not an event this build understands; the caller
// shows that as a status line rather than dropping it.
export function parseStreamEvent(data: unknown): StreamEventData | null {
  if (typeof data !== 'string') return null
  let v: unknown
  try {
    v = JSON.parse(data)
  } catch {
    return null
  }
  if (typeof v !== 'object' || v === null) return null
  const e = v as Record<string, unknown>
  if (typeof e.text !== 'string' || typeof e.time !== 'string') return null
  if (e.kind !== 'message' && e.kind !== 'status') return null
  return { time: e.time, kind: e.kind, text: e.text }
}
