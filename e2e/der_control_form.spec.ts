import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer } from './setup';
import { ADMIN_KEY } from './adminKey';

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer({
    SEP2_BOOT_FIXTURE: join(__dirname, 'fixtures', 'der-control.yaml'),
    SEP2_PEN: '12345',
  });
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders({ 'Authorization': `Bearer ${ADMIN_KEY}` });
});

test('the function fields sit in three columns, one on a narrow window, and the start picker opens at now', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(baseUrl + `/ui/control?token=${ADMIN_KEY}`);
  await page.waitForLoadState('domcontentloaded');

  const grid = page.getByTestId('control-functions');
  await expect(page.getByTestId('control-function-opModConnect')).toBeVisible();
  const columns = async () =>
    (await grid.evaluate((el) => getComputedStyle(el).gridTemplateColumns)).trim().split(/\s+/).length;
  expect(await columns()).toBe(3);

  await page.setViewportSize({ width: 600, height: 900 });
  expect(await columns()).toBe(1);

  const before = Date.now();
  await page.getByLabel('Start now').uncheck();
  const picked = await page.locator('#controlStartAt').inputValue();
  expect(picked).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$/);
  const pickedMs = await page.evaluate((v) => new Date(v).getTime(), picked);
  expect(Math.abs(pickedMs - before)).toBeLessThan(60_000);
});
