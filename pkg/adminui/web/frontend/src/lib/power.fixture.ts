// A GET /api/derms/power reply as the server sends it, for the power tests.
export const wireAnswer = (over: Record<string, unknown> = {}) => ({
  scope: 'all',
  aggregators: ['AGG1'],
  windowStart: 1000,
  windowEnd: 1060,
  bucketSeconds: 5,
  chart: { unit: 'W', series: [] },
  energyChart: {
    unit: 'kWh',
    series: [
      { name: 'Discharged', points: [[1000000, 0.5]] },
      { name: 'Charged', points: [[1000000, -0.25]] },
    ],
  },
  devices: [{ lfdi: 'D1', drawn: true, coveredBuckets: 1, directionUnknown: false, concurrentMirrors: false, excludedReadings: 0, nonDERWatts: false }],
  notReporting: [],
  totalGapBuckets: 0,
  readingsExpired: false,
  latestW: 2000,
  latestAt: 1055,
  latestMissing: [],
  ...over,
})
