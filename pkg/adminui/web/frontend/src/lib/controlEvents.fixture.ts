// A GET /api/derms/control-events reply as the server sends it, for the
// timeline tests. END is the shared range end in epoch seconds.
import type { ControlEvent } from './controlEvents'

export const END = 1_800_000_000

export const wireEvent = (over: Partial<ControlEvent> = {}): ControlEvent => ({
  group: 'AGG1',
  groupKind: 'aggregator',
  scope: 'device',
  deviceIDs: ['D1'],
  mrid: 'M1',
  origin: 'admin',
  direction: 'discharge',
  targetW: 5000,
  modes: ['opModTargetW'],
  status: 'active',
  start: END - 1800,
  end: END - 900,
  ...over,
})

// aheadEnd is the look-ahead end the route sends; null leaves it out, as an
// older server does.
export const wireEvents = (events: ControlEvent[], aheadEnd: number | null = END + 3600) => ({
  windowStart: END - 3600,
  windowEnd: END,
  ...(aheadEnd === null ? {} : { aheadEnd }),
  events,
})
