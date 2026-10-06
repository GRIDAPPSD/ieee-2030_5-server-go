// dashboard.ts holds the shape of the dashboard SSE payload
// (internal/adminplane/dashboard.go's DashboardData) and the connection
// logic that keeps it flowing: an auth-ticket exchange followed by an
// EventSource on /dashboard/events, which pushes an update every 5
// seconds.

import { postJSON } from './api'
import { commsOnlineCount } from './comms'

// The server's comms states for one device: a request seen within the
// offline threshold, one seen but older, none since the server started, or
// no recorder wired at all.
export type CommsState = 'online' | 'offline' | 'not_seen' | 'unknown'

export interface DashboardDevice {
  sfdi: string
  lfdi: string
  href: string
  // null when the EndDevice has no enabled flag.
  enabled: boolean | null
  // Server-clock time (RFC 3339, UTC) of the device's last request, or null
  // when none was recorded since the server started.
  lastRequest: string | null
  comms: CommsState
  // True when the DER status below is what the device last reported rather
  // than a live reading (comms offline or not seen since start).
  lastKnown: boolean
  // Never null: a device with no DERs has an empty list.
  ders: DashboardDER[]
  // Set when this device's DER data could not be read in full. An empty
  // ders with derError set means unreadable, not "no DERs".
  derError?: string
}

// DerConnect is one connection-status field as the DER reported it
// (internal/derstatus Connect). Unix times are seconds.
export interface DerConnect {
  // The DERStatus element it was read from: connectStatus,
  // genConnectStatus or storConnectStatus.
  source: string
  raw: number
  since: number
  connected: boolean
  // Present only for connectStatus (2023).
  energized?: boolean
  available: boolean
  operating: boolean
  test: boolean
  fault: boolean
  reservedBits: number
  // The storConnectStatus of a DER that reports it beside genConnectStatus.
  also?: DerConnect
}

export interface DerInverter {
  code: number
  since: number
}

// DashboardDER is one DER's decoded DERStatus. When reported is false the
// server holds no DERStatus and the other fields mean nothing.
export interface DashboardDER {
  id: string
  reported: boolean
  connect: DerConnect | null
  inverter: DerInverter | null
  readingTime: number
  ageSeconds: number
  stale: boolean
  noReadingTime: boolean
  clockAhead: boolean
}

export interface DashboardData {
  timestamp: string
  deviceCount: number
  mupCount: number
  tlsMode: string
  uptime: string
  devices: DashboardDevice[] | null
  // The threshold each device's comms was judged against.
  commsOfflineAfterSeconds: number
  // Set when the server could not read the device list; devices is then null.
  error?: string
}

// HISTORY_LIMIT caps the in-memory activity series at 5 minutes of
// 5-second samples, so a long-lived tab does not grow the chart's data
// array without bound.
export const HISTORY_LIMIT = 60

export interface HistoryPoint {
  time: string
  devices: number
  mups: number
  commsOnline: number
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
  const next = [...history, {
      time: data.timestamp,
      devices: data.deviceCount,
      mups: data.mupCount,
      commsOnline: commsOnlineCount(data.devices ?? []),
    }]
  return next.length > HISTORY_LIMIT ? next.slice(next.length - HISTORY_LIMIT) : next
}
