// dashboard.ts holds the shape of the dashboard SSE payload
// (internal/server/dashboard.go's DashboardData) and the connection
// logic that keeps it flowing: an auth-ticket exchange followed by an
// EventSource on /dashboard/events, which pushes an update every 5
// seconds.

import { postJSON } from './api'

export interface DashboardDevice {
  sfdi: string
  lfdi: string
  href: string
  enabled: boolean
}

export interface DashboardData {
  timestamp: string
  deviceCount: number
  mupCount: number
  tlsMode: string
  uptime: string
  devices: DashboardDevice[] | null
}

// HISTORY_LIMIT caps the in-memory activity series at 5 minutes of
// 5-second samples, so a long-lived tab does not grow the chart's data
// array without bound.
export const HISTORY_LIMIT = 60

export interface HistoryPoint {
  time: string
  devices: number
  mups: number
}

const RECONNECT_DELAY_MS = 3000

// connectDashboard opens the SSE stream and calls onData for every
// update. EventSource cannot carry an Authorization header, so the
// stream is authorized by a one-time-use ticket from POST /auth/ticket
// (internal/auth/ticket.go) passed as a query parameter; a dropped
// stream needs a FRESH ticket, which is why the retry re-runs the whole
// exchange rather than reusing the old query string.
//
// A failed ticket exchange still attempts the stream without one: on a
// loopback admin listener, or with a valid session cookie, the stream
// authorizes without a ticket, and a hard failure here would blank the
// whole dashboard for a request that would have succeeded.
export function connectDashboard(onData: (data: DashboardData) => void): () => void {
  let source: EventSource | null = null
  let retry: ReturnType<typeof setTimeout> | null = null
  let closed = false

  const open = (ticket: string) => {
    if (closed) return
    const url = ticket ? `/dashboard/events?ticket=${encodeURIComponent(ticket)}` : '/dashboard/events'
    source = new EventSource(url)
    source.onmessage = (event: MessageEvent<string>) => {
      try {
        onData(JSON.parse(event.data) as DashboardData)
      } catch (err) {
        console.warn('dashboard: discarding an unparseable SSE frame:', err)
      }
    }
    source.onerror = () => {
      source?.close()
      source = null
      retry = setTimeout(exchangeAndOpen, RECONNECT_DELAY_MS)
    }
  }

  const exchangeAndOpen = () => {
    if (closed) return
    // Through api.ts's helper rather than fetch: the same-origin
    // credential mode, the transport-failure case and the JSON decode all
    // belong to that one client. The endpoint reads no request body, so
    // the empty object is only there to satisfy the helper's shape.
    void postJSON<{ ticket?: string }>('/auth/ticket', {}).then((res) => {
      open(res.ok ? (res.data.ticket ?? '') : '')
    })
  }

  exchangeAndOpen()

  return () => {
    closed = true
    if (retry !== null) clearTimeout(retry)
    source?.close()
    source = null
  }
}

// appendHistory returns the activity series with one sample added,
// trimmed to HISTORY_LIMIT. Pure so the chart panel and its test share
// the same trimming rule.
export function appendHistory(history: HistoryPoint[], data: DashboardData): HistoryPoint[] {
  const next = [...history, { time: data.timestamp, devices: data.deviceCount, mups: data.mupCount }]
  return next.length > HISTORY_LIMIT ? next.slice(next.length - HISTORY_LIMIT) : next
}
