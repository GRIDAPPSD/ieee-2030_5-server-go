import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer } from './setup';
import { ADMIN_KEY } from './adminKey';

// #1190: the Overview events timeline under the power chart. The power and
// control-events routes are answered with fixed bodies, so the page's lanes,
// bars and markers are read from a known set. When SCREENSHOT_DIR is set the
// test also writes the two layout screenshots there.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const LFDI_A = 'A'.repeat(40);
const LFDI_B = 'B'.repeat(40);
const WINDOW = 900;
const BUCKET = 30;

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer();
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders(AUTH);
});

const energy = (key: string, name: string, base: number, now: number) => {
  const buckets = (sign: number) =>
    Array.from({ length: 24 }, (_, i) => [Math.floor(now / 1000 / BUCKET) * BUCKET - (24 - i) * BUCKET, sign * (base + (i % 5) * 0.01)]);
  return {
    key,
    name,
    latestW: base * 1000,
    latestMissing: [],
    discharged: buckets(1).slice(0, 12),
    charged: buckets(-1).slice(12),
  };
};

function events(now: number) {
  const s = Math.floor(now / 1000);
  const common = { scope: 'device', deviceIDs: ['D1'], modes: ['opModTargetW'] };
  return {
    windowStart: s - WINDOW,
    windowEnd: s,
    aheadEnd: s + WINDOW,
    events: [
      { ...common, group: LFDI_A, groupKind: 'aggregator', mrid: 'E1', origin: 'derms', direction: 'discharge', targetW: 5000, status: 'active', start: s - 600, end: s - 120 },
      { ...common, group: LFDI_B, groupKind: 'aggregator', mrid: 'E2', origin: 'admin', direction: 'charge', targetW: -3000, status: 'scheduled', start: s + 150, end: s + 600 },
      { ...common, group: 'direct', groupKind: 'direct', mrid: 'E3', origin: 'admin', direction: 'discharge', targetW: 2000, status: 'cancelled', start: s - 840, end: s - 540 },
    ],
  };
}

test('the Overview draws a lane per aggregator and Direct with each event as a labelled bar', async ({ page }) => {
  const now = Date.now();
  await page.route('**/api/device-profiles', (route) =>
    route.fulfill({
      json: {
        profiles: [
          { lfdi: LFDI_A, name: 'North feeder', kind: 'aggregator' },
          { lfdi: LFDI_B, name: 'South feeder', kind: 'aggregator' },
        ],
      },
    }),
  );
  await page.route('**/api/derms/power**', (route) =>
    route.fulfill({
      json: {
        scope: 'all',
        aggregators: [LFDI_A, LFDI_B],
        bucketSeconds: BUCKET,
        energyChart: { unit: 'kWh', series: [{ name: 'Discharged', points: [[now - 400000, 0.4]] }, { name: 'Charged', points: [[now - 100000, -0.2]] }] },
        aggregatorEnergy: [energy(LFDI_A, LFDI_A, 0.4, now), energy(LFDI_B, LFDI_B, 0.2, now), energy('direct', 'direct', 0.1, now)],
        devices: [],
        notReporting: [],
        latestW: 4000,
        latestAt: Math.floor(now / 1000),
        latestMissing: [],
      },
    }),
  );
  await page.route('**/api/derms/control-events**', (route) => route.fulfill({ json: events(now) }));

  await page.goto(baseUrl + `/ui/overview?token=${ADMIN_KEY}`);
  const timeline = page.getByTestId('events-timeline');
  await expect(timeline).toBeVisible();
  await expect(timeline.getByTestId('events-lane-label')).toHaveText(['North feeder', 'South feeder', 'Direct']);
  const bars = timeline.getByTestId('events-bar');
  await expect(bars).toHaveCount(3);
  await expect(timeline.getByTestId('events-lane').nth(0)).toContainText('discharge 5.0 kW');
  await expect(timeline.getByTestId('events-lane').nth(1)).toContainText('charge 3.0 kW');
  await expect(timeline.getByTestId('events-lane').nth(2)).toContainText('discharge 2.0 kW');
  await expect(timeline.getByTestId('events-derms')).toHaveCount(1);
  await expect(bars.nth(1)).toHaveAttribute('data-status', 'scheduled');
  await expect(page.getByTestId('chart-now')).toBeVisible();
  const nowLine = await timeline.locator('.ev-now').first().boundingBox();
  const future = await bars.nth(1).boundingBox();
  expect(future!.x).toBeGreaterThan(nowLine!.x);
  await expect(bars.nth(2)).toHaveAttribute('data-status', 'cancelled');

  // On a phone every bar keeps its direction word on screen, and the DERMS
  // marker does not sit over it.
  await page.setViewportSize({ width: 400, height: 900 });
  await expect(timeline).toBeVisible();
  const words = ['dis', 'cha', 'dis'];
  for (let i = 0; i < 3; i++) {
    const lead = bars.nth(i).locator('.ev-lead');
    await expect(lead).toBeVisible();
    const box = (await lead.boundingBox())!;
    const bar = (await bars.nth(i).boundingBox())!;
    expect(box.width).toBeGreaterThan(12);
    expect(box.x + box.width).toBeLessThanOrEqual(bar.x + bar.width + 1);
    expect((await lead.innerText()).toLowerCase().startsWith(words[i])).toBe(true);
  }
  const marker = (await timeline.getByTestId('events-derms').boundingBox())!;
  const derms = (await bars.nth(0).locator('.ev-lead').boundingBox())!;
  expect(marker.x >= derms.x + derms.width - 1 || marker.y + marker.height <= derms.y + 1).toBe(true);

  const dir = process.env.SCREENSHOT_DIR;
  if (dir) {
    await page.setViewportSize({ width: 1600, height: 1000 });
    await expect(page.getByTestId('descriptor-chart')).toBeVisible();
    await page.screenshot({ path: join(dir, 'after-desktop-events.png') });
    await page.setViewportSize({ width: 400, height: 900 });
    await expect(timeline).toBeVisible();
    await page.screenshot({ path: join(dir, 'after-phone-events.png'), fullPage: true });
  }
});

test('the Overview shows no timeline when the server has no control-events route', async ({ page }) => {
  await page.route('**/api/derms/control-events**', (route) => route.fulfill({ status: 404, json: { error: 'not found' } }));
  await page.goto(baseUrl + `/ui/overview?token=${ADMIN_KEY}`);
  await expect(page.getByTestId('power-card')).toBeVisible();
  await expect(page.getByTestId('descriptor-chart')).toBeVisible({ timeout: 15000 });
  await expect(page.getByTestId('events-timeline')).toHaveCount(0);
});
