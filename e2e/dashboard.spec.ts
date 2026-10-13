import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer } from './setup';

let baseUrl: string;

// One device, one FSA and one DERProgram (e2e/fixtures/der-control.yaml),
// plus a PEN so POST /api/der/controls does not answer 503: the DER
// control test below needs both.
test.beforeAll(async () => {
  baseUrl = await startServer({
    SEP2_BOOT_FIXTURE: join(__dirname, 'fixtures', 'der-control.yaml'),
    SEP2_PEN: '12345',
  });
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

  await page.getByTestId('tab-devices').click();
  const deviceTable = page.locator('#deviceTable');
  await expect(deviceTable).toBeVisible();

  await page.getByTestId('tab-overview').click();
  const mupCount = page.locator('#mupCount');
  await expect(mupCount).toBeVisible();
});

// 3. Certificate generation form works
test('certificate generation form works', async ({ page }) => {
  await page.goto(baseUrl + '/ui/certificates?token=e2e-test-key');
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

// 4. DER control panel creates a control through the admin API (criterion 8;
// e2e/fixtures/der-control.yaml seeds the device, FSA and DER program this
// picks).
test('DER control panel creates a control and lists it in the table', async ({ page }) => {
  await page.goto(baseUrl + '/ui/control?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  await page.locator('#controlDevice').selectOption({ label: '222222222222' });
  await page.locator('#controlProgram').selectOption({ label: 'e2e der control program' });

  const controlSelect = page.locator('#controlType');
  await expect(controlSelect).toBeVisible();
  await controlSelect.selectOption('disconnect');
  await page.locator('#controlDuration').fill('5');

  const sendButton = page.getByRole('button', { name: 'Send' });
  await expect(sendButton).toBeVisible();
  await sendButton.click();
  await page.getByRole('button', { name: 'Confirm' }).click();

  const result = page.locator('#controlResult');
  await expect(result).toContainText(/[0-9A-F]{32}/, { timeout: 5000 });
  const mrid = (await result.textContent())?.match(/[0-9A-F]{32}/)?.[0];
  expect(mrid).toBeTruthy();

  const row = page.locator(`[data-testid="der-control-row"][data-mrid="${mrid}"]`);
  await expect(row).toBeVisible();
  await expect(row).toContainText('disconnect');
});

// 5. Dashboard renders all sections correctly
test('dashboard renders all UI sections', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  // Overview card
  await expect(page.getByRole('heading', { name: 'Registered devices' })).toBeVisible();
  await expect(page.getByText('Connected Devices')).toHaveCount(0);
  await expect(page.getByText('Mirror Usage Points')).toBeVisible();

  // Server info card
  await expect(page.getByText('SERVER INFO')).toBeVisible();
  await expect(page.getByText('TLS Cipher')).toBeVisible();
  await expect(page.getByText('IEEE 2030.5-2018')).toBeVisible();

  // Certificate management card
  await page.getByTestId('tab-certificates').click();
  await expect(page.getByText('CERTIFICATE MANAGEMENT')).toBeVisible();
  await expect(page.locator('#hwSerial')).toBeVisible();

  // DER control card
  await page.getByTestId('tab-control').click();
  await expect(page.getByText('SEND DER CONTROL')).toBeVisible();
  await expect(page.locator('#controlType')).toBeVisible();

  // Device table
  await page.getByTestId('tab-devices').click();
  await expect(page.getByText('END DEVICES')).toBeVisible();
  await expect(page.locator('th:has-text("SFDI")')).toBeVisible();
  await expect(page.locator('th:has-text("LFDI")')).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'Enabled' })).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'Comms' })).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'Last request' })).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'DER connection (reported)' })).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'Inverter state (reported)' })).toBeVisible();
  await expect(page.getByText('ONLINE', { exact: true })).toHaveCount(0);
  await expect(page.getByText('OFFLINE', { exact: true })).toHaveCount(0);

  // Activity chart
  await page.getByTestId('tab-overview').click();
  await expect(page.getByText('DEVICE ACTIVITY')).toBeVisible();
  await expect(page.locator('#activityChart')).toBeVisible();
});

// 6. A fresh server has heard from no client, so every device reads Not seen
// with no last-request time, and the Overview counts none online. The
// harness issues only admin-plane requests, which the comms recorder does
// not see, so this holds however many specs ran before it.
test('a fresh server shows Not seen for every device and 0 online', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  await expect(page.locator('#commsOnline')).toHaveText('0 of 1');

  await page.getByTestId('tab-devices').click();
  const comms = page.getByTestId('device-comms');
  await expect(comms).toHaveCount(1);
  await expect(comms.first()).toHaveText('Not seen');
  await expect(page.getByTestId('device-last-request').first()).toHaveText('');
});
