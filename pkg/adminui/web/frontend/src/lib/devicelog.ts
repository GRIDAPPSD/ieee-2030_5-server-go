// The wire shape of GET /api/device-logs/{lfdi} and the row the Logs drawer
// shows for each line. The server stores each line as a JSON object and
// returns it unchanged, so every field is read defensively and rendered as
// text only.

export interface LogEntry {
  cursor: string
  source: string
  line: Record<string, unknown>
}

export interface LogPage {
  entries: LogEntry[]
  nextBefore: string | null
  nextAfter: string | null
  dropped: number
}

export interface LogRow {
  cursor: string
  source: string
  t: string
  kind: string
  actor: string
  method: string
  path: string
  status: number | null
  durMs: number | null
  detail: string
}

// parsePage returns null for a body that is not a page, so the caller shows
// a shape error instead of an empty log.
export function parsePage(data: unknown): LogPage | null {
  if (typeof data !== 'object' || data === null) return null
  const d = data as Record<string, unknown>
  if (!Array.isArray(d.entries)) return null
  const entries: LogEntry[] = []
  for (const e of d.entries) {
    if (typeof e !== 'object' || e === null) return null
    const o = e as Record<string, unknown>
    if (typeof o.cursor !== 'string' || o.cursor === '') return null
    const line = typeof o.line === 'object' && o.line !== null && !Array.isArray(o.line) ? o.line : {}
    entries.push({ cursor: o.cursor, source: typeof o.source === 'string' ? o.source : '', line: line as Record<string, unknown> })
  }
  const cursor = (v: unknown) => (typeof v === 'string' && v !== '' ? v : null)
  return {
    entries,
    nextBefore: cursor(d.nextBefore),
    nextAfter: cursor(d.nextAfter),
    dropped: typeof d.dropped === 'number' ? d.dropped : 0,
  }
}

function text(v: unknown): string {
  return typeof v === 'string' || typeof v === 'number' ? String(v) : ''
}

function num(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}

// logRow flattens one entry. detail is what the line says beyond a request:
// a LogEvent's function set, code and details, or a refusal's reason.
export function logRow(e: LogEntry): LogRow {
  const l = e.line
  const parts: string[] = []
  if (l.functionSet !== undefined) parts.push(`function set ${text(l.functionSet)}`)
  if (l.logEventCode !== undefined) parts.push(`code ${text(l.logEventCode)}`)
  if (l.details !== undefined) parts.push(text(l.details))
  if (l.reason !== undefined) parts.push(text(l.reason))
  return {
    cursor: e.cursor,
    source: e.source,
    t: text(l.t),
    kind: text(l.kind),
    actor: text(l.actor),
    method: text(l.method),
    path: text(l.path),
    status: num(l.status),
    durMs: num(l.dur_ms),
    detail: parts.join(', '),
  }
}

export type KindFilter = 'all' | 'request' | 'logevent' | 'refused'

// visible applies the drawer's two filters to the lines already loaded.
// Errors only keeps status 400 and up, so a LogEvent line, which has no
// status, is hidden by it.
export function visible(rows: LogRow[], kind: KindFilter, errorsOnly: boolean): LogRow[] {
  return rows.filter((r) => (kind === 'all' || r.kind === kind) && (!errorsOnly || (r.status !== null && r.status >= 400)))
}

// mergeNewer puts a poll's lines in front of the held ones, newest first,
// and skips a cursor already held.
export function mergeNewer(held: LogEntry[], page: LogEntry[]): LogEntry[] {
  const have = new Set(held.map((e) => e.cursor))
  return [...page.filter((e) => !have.has(e.cursor)), ...held]
}

// mergeOlder appends an Older page behind the held lines, skipping a cursor
// already held.
export function mergeOlder(held: LogEntry[], page: LogEntry[]): LogEntry[] {
  const have = new Set(held.map((e) => e.cursor))
  return [...held, ...page.filter((e) => !have.has(e.cursor))]
}

export function logPath(lfdi: string, query: string): string {
  return `/api/device-logs/${encodeURIComponent(lfdi)}${query}`
}
