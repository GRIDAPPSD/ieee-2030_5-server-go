import { test, expect } from '@playwright/test';
import { startServer, stopServer } from './setup';
import { AUTH, fillBlock, openDerms, seedFleet } from './fixtures/grid';

// #1158: enter timed grid blocks on the DERMS tab. The aggregator, its two
// devices and the pairs are seeded through the admin API, the way the Devices
// tab stores them; the batch itself is entered and confirmed only through the
// page, and the server's answer is what the page shows. A seeded batch makes
// the overlap case: the server, not the page, decides the conflict.

let baseUrl: string;

test.beforeEach(async ({ page }) => {
  baseUrl = await startServer();
  await page.setExtraHTTPHeaders(AUTH);
});

test.afterEach(() => {
  stopServer();
});

test('a two-row back-to-back batch is accepted, with each row split to the aggregator', async ({ page, request }) => {
  await seedFleet(request, baseUrl);
  const before = await request.get(`${baseUrl}/api/derms/fleets`, { headers: AUTH });
  expect((await before.json()).length).toBe(1);

  await openDerms(page, baseUrl);
  await fillBlock(page, 0, { to: true, x: '50', minutes: '60' });
  await page.getByRole('button', { name: 'Add row' }).click();
  await fillBlock(page, 1, { to: false, x: '20', minutes: '30' });
  await page.getByRole('button', { name: 'Review' }).click();

  const lines = page.getByTestId('grid-confirm-line');
  await expect(lines).toHaveCount(2);
  await expect(lines.nth(0)).toContainText('DISCHARGE 50,000 W TO the grid');
  await expect(lines.nth(0)).toContainText('Wire, batch total: opModTargetW = +50 x 10^3 W');
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
  await seedFleet(request, baseUrl);
  const seeded = await request.post(`${baseUrl}/api/derms/grid-requests`, {
    headers: AUTH,
    data: { rows: [{ direction: 'discharge', x: 10, y: 3, durationSeconds: 3600 }] },
  });
  expect(seeded.status()).toBe(201);

  await openDerms(page, baseUrl);
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
