import { describe, expect, it } from 'vitest'
import { commsClass, commsLabel, commsOnlineCount, formatAge } from './comms'
import type { DashboardDevice } from './dashboard'

const NOW = Date.parse('2026-10-05T12:00:00Z')

describe('commsLabel and commsClass', () => {
  it('maps each server state to its own label and class', () => {
    expect(['online', 'offline', 'not_seen', 'unknown'].map(commsLabel)).toEqual(['Online', 'Offline', 'Not seen', 'Unknown'])
    expect(['online', 'offline', 'not_seen', 'unknown'].map(commsClass)).toEqual(['online', 'offline', 'not-seen', 'comms-unknown'])
  })

  it('shows a state this build does not know as the raw value, unstyled as a known one', () => {
    expect(commsLabel('degraded')).toBe('degraded')
    expect(commsClass('degraded')).toBe('comms-unknown')
  })
})

describe('commsOnlineCount', () => {
  it('counts only online devices', () => {
    const dev = (comms: DashboardDevice['comms']): DashboardDevice => ({
      sfdi: '1',
      lfdi: '1',
      href: '/edev/1',
      enabled: null,
      lastRequest: null,
      comms,
      lastKnown: false,
      ders: [],
    })
    expect(commsOnlineCount([dev('online'), dev('offline'), dev('not_seen'), dev('unknown'), dev('online')])).toBe(2)
    expect(commsOnlineCount([])).toBe(0)
  })
})

describe('formatAge', () => {
  const at = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString()

  it.each([
    [0, 'just now'],
    [4, 'just now'],
    [5, '5s ago'],
    [59, '59s ago'],
    [60, '1m ago'],
    [3599, '59m ago'],
    [3600, '1h ago'],
    [86399, '23h ago'],
    [86400, '1d ago'],
    [3 * 86400 + 5, '3d ago'],
  ])('renders %i seconds as %s', (seconds, want) => {
    expect(formatAge(at(seconds), NOW)).toBe(want)
  })

  it('renders a time ahead of the browser clock as just now, never a negative age', () => {
    expect(formatAge(at(-30), NOW)).toBe('just now')
  })

  it('is empty for a device never seen', () => {
    expect(formatAge(null, NOW)).toBe('')
  })

  it('returns an unparseable time unchanged so nothing the server sent is hidden', () => {
    expect(formatAge('not a time', NOW)).toBe('not a time')
  })
})
