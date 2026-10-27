import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert } from './fixtures/device_cert';

// #1158: enter timed grid blocks on the DERMS tab. The aggregator, its two
// devices and the pairs are seeded through the admin API, the way the Devices
// tab stores them; the batch itself is entered and confirmed only through the
// page, and the server's answer is what the page shows. A seeded batch makes
// the overlap case: the server, not the page, decides the conflict.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const SERVER = join(__dirname, '..', 'sep2server');

let baseUrl: string;

test.beforeEach(async ({ page }) => {
  baseUrl = await startServer();
  await page.setExtraHTTPHeaders(AUTH);
});

test.afterEach(() => {
  stopServer();
});

async function register(request: APIRequestContext, name: string, kind: string): Promise<string> {
  const cert = mintDeviceCert(SERVER, getCertDir(), name);
  const info = await request.post(`${baseUrl}/api/certs/info`, {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: cert.certPEM,
  });
  expect(info.status()).toBe(200);
  const { sfdi, lfdi } = await info.json();
  const added = await request.post(`${baseUrl}/api/devices`, {
    headers: AUTH,
    data: { sfdi, lfdi, name, kind, pin: 424242, enabled: true },
  });
  expect(added.status()).toBe(201);
  return String(lfdi).toUpperCase();
}

// seedFleet registers an aggregator with two managed devices.
async function seedFleet(request: APIRequestContext) {
  const aggregator = await register(request, 'grid-agg', 'aggregator');
  for (const name of ['grid-dev-a', 'grid-dev-b']) {
    const managed = await register(request, name, 'device');
    const pair = await request.post(`${baseUrl}/api/management-pairs`, {
      headers: AUTH,
      data: { managerLFDI: aggregator, managedLFDI: managed },
    });
    expect(pair.status()).toBeLessThan(300);
  }
}

async function openDerms(page: Page) {
  await page.goto(`${baseUrl}/ui/derms?token=${ADMIN_KEY}`);
  await expect(page.getByTestId('grid-request-pane')).toBeVisible({ timeout: 10000 });
}

async function fillBlock(page: Page, n: number, v: { to: boolean; x: string; minutes: string }) {
  const row = page.getByTestId('grid-row').nth(n);
  await row.getByRole('radio', { name: v.to ? /^To grid/ : /^From grid/ }).check();
  await row.getByTestId('grid-x').fill(v.x);
  await row.getByTestId('grid-duration').fill(v.minutes);
}

test('a two-row back-to-back batch is accepted, with each row split to the aggregator', async ({ page, request }) => {
  await seedFleet(request);
  const before = await request.get(`${baseUrl}/api/derms/fleets`, { headers: AUTH });
  expect((await before.json()).length).toBe(1);

  await openDerms(page);
  await fillBlock(page, 0, { to: true, x: '50', minutes: '60' });
  await page.getByRole('button', { name: 'Add row' }).click();
  await fillBlock(page, 1, { to: false, x: '20', minutes: '30' });
  await page.getByRole('button', { name: 'Review' }).click();

  const lines = page.getByTestId('grid-confirm-line');
  await expect(lines).toHaveCount(2);
  await expect(lines.nth(0)).toContainText('DISCHARGE 50,000 W TO the grid');
  await expect(lines.nth(0)).toContainText('opModTargetW = +50 x 10^3 W');
  await expect(lines.nth(1)).toContainText('CHARGE 20,000 W FROM the grid, starts when block 1 ends');
  await expect(lines.nth(1)).toContainText('opModTargetW = -20 x 10^3 W');
  await page.getByRole('button', { name: 'Confirm' }).click();

  await expect(page.getByTestId('grid-created')).toContainText('with 2 blocks', { timeout: 10000 });
  const shares = page.getByTestId('grid-share');
  await expect(shares).toHaveCount(2);
  await expect(shares.nth(0)).toContainText('50,000 W (opModTargetW)');
  await expect(shares.nth(1)).toContainText('-20,000 W (opModTargetW)');
  // Devices without a nameplate weigh 5 kW, and the server says so.
  await expect(page.getByTestId('grid-warnings')).toBeVisible();
});

test('a batch overlapping a live one is refused whole and the row stays marked as typed', async ({ page, request }) => {
  await seedFleet(request);
  const seeded = await request.post(`${baseUrl}/api/derms/grid-requests`, {
    headers: AUTH,
    data: { rows: [{ direction: 'discharge', x: 10, y: 3, durationSeconds: 3600 }] },
  });
  expect(seeded.status()).toBe(201);

  await openDerms(page);
  await fillBlock(page, 0, { to: false, x: '30', minutes: '15' });
  await page.getByRole('button', { name: 'Review' }).click();
  await page.getByRole('button', { name: 'Confirm' }).click();

  const refusal = page.getByTestId('grid-row').nth(0).getByTestId('grid-row-refusal');
  await expect(refusal).toContainText('overlaps a live block of batch', { timeout: 10000 });
  await expect(refusal).toContainText('overlapping_request');
  await expect(page.getByTestId('grid-form-error')).toContainText('rows_conflict');
  await expect(page.getByTestId('grid-row').nth(0).getByTestId('grid-x')).toHaveValue('30');
  await expect(page.getByTestId('grid-created')).toHaveCount(0);
});
