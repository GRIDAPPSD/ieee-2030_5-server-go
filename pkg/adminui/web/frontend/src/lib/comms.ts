// comms.ts turns the dashboard's per-device comms fields (the server's
// activity.Comms values and lastRequest time) into what the Devices tab and
// the Overview show. Comms is about requests reaching this server; it says
// nothing about whether the device is enabled or whether its DER is
// connected, which are separate columns.

import type { CommsState, DashboardDevice } from './dashboard'

export const COMMS_LABELS: Record<CommsState, string> = {
  online: 'Online',
  offline: 'Offline',
  not_seen: 'Not seen',
  unknown: 'Unknown',
}

// commsLabel falls back to the raw wire value for a state this build does
// not know, so a newer server's value is shown rather than hidden.
export function commsLabel(comms: string): string {
  return (COMMS_LABELS as Record<string, string>)[comms] ?? comms
}

// The stylesheet class for each state. Not seen is deliberately not the red
// of Offline: a device the server has never heard from is not a device that
// went quiet.
export function commsClass(comms: string): string {
  switch (comms) {
    case 'online':
      return 'online'
    case 'offline':
      return 'offline'
    case 'not_seen':
      return 'not-seen'
    default:
      return 'comms-unknown'
  }
}

export function commsOnlineCount(devices: DashboardDevice[]): number {
  return devices.filter((d) => d.comms === 'online').length
}

// formatAge renders how long before nowMs the RFC 3339 time iso was, as a
// coarse relative age. The time is the server's clock and nowMs the
// browser's, so a skew can put it slightly in the future; that reads as
// "just now" rather than a negative age. A null time (never seen) is an
// empty string, and a string that does not parse is returned as it came so
// nothing the server sent is hidden.
export function formatAge(iso: string | null, nowMs: number): string {
  if (iso === null) return ''
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return iso
  const seconds = Math.max(0, Math.floor((nowMs - then) / 1000))
  if (seconds < 5) return 'just now'
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}
