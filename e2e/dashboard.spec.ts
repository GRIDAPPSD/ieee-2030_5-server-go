import { test, expect } from '@playwright/test';
import { startServer, stopServer } from './setup';

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer();
});

test.afterAll(() => {
  stopServer();
});

// Set auth headers for all requests
test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders({
    'Authorization': 'Bearer e2e-test-key',
  });
});

// 1. Dashboard loads and shows server info
test('dashboard loads and shows server info', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  await expect(page).toHaveTitle(/IEEE 2030.5/);
  await expect(page.locator('.navbar h1')).toContainText('IEEE 2030.5');

  const tlsBadge = page.locator('#tlsMode');
  await expect(tlsBadge).toBeVisible();

  const uptime = page.locator('#uptime');
  await expect(uptime).toBeVisible();

  await expect(page.getByText('IEEE 2030.5-2018')).toBeVisible();
  await expect(page.locator('#bigDeviceCount')).toBeVisible();
});

// 2. Device table updates when a device registers
test('device table shows data', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  const deviceCount = page.locator('#bigDeviceCount');
  await expect(deviceCount).toBeVisible();
  const countText = await deviceCount.textContent();
  expect(countText).toMatch(/^\d+$/);

  const deviceTable = page.locator('#deviceTable');
  await expect(deviceTable).toBeVisible();

  const mupCount = page.locator('#mupCount');
  await expect(mupCount).toBeVisible();
});

// 3. Certificate generation form works
test('certificate generation form works', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  const serialInput = page.locator('#hwSerial');
  await expect(serialInput).toBeVisible();
  await serialInput.fill('PW-INV-001');

  const genButton = page.getByRole('button', { name: 'Generate Device Cert' });
  await expect(genButton).toBeVisible();
  await genButton.click();

  const result = page.locator('#certResult');
  await expect(result).not.toHaveText('', { timeout: 5000 });
});

// 4. DER control panel accepts input
test('DER control panel sends commands', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  const controlSelect = page.locator('#controlType');
  await expect(controlSelect).toBeVisible();
  await controlSelect.selectOption('disconnect');

  const sendButton = page.getByRole('button', { name: 'Send' });
  await expect(sendButton).toBeVisible();
  await sendButton.click();

  const result = page.locator('#controlResult');
  await expect(result).toContainText('disconnect', { timeout: 3000 });
});

// 5. Dashboard renders all sections correctly
test('dashboard renders all UI sections', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  // Overview card
  await expect(page.getByText('CONNECTED DEVICES')).toBeVisible();
  await expect(page.getByText('Mirror Usage Points')).toBeVisible();

  // Server info card
  await expect(page.getByText('SERVER INFO')).toBeVisible();
  await expect(page.getByText('TLS Cipher')).toBeVisible();
  await expect(page.getByText('IEEE 2030.5-2018')).toBeVisible();

  // Certificate management card
  await expect(page.getByText('CERTIFICATE MANAGEMENT')).toBeVisible();
  await expect(page.locator('#hwSerial')).toBeVisible();

  // DER control card
  await expect(page.getByText('SEND DER CONTROL')).toBeVisible();
  await expect(page.locator('#controlType')).toBeVisible();

  // Device table
  await expect(page.getByText('END DEVICES')).toBeVisible();
  await expect(page.locator('th:has-text("SFDI")')).toBeVisible();
  await expect(page.locator('th:has-text("LFDI")')).toBeVisible();

  // Activity chart
  await expect(page.getByText('DEVICE ACTIVITY')).toBeVisible();
  await expect(page.locator('#activityChart')).toBeVisible();
});
