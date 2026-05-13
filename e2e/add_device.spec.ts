import { test, expect } from '@playwright/test';
import { execFileSync } from 'child_process';
import { join } from 'path';
import { readFileSync } from 'fs';
import { startServer, stopServer, getCertDir } from './setup';

// IEEE-095: Add EndDevice flow.
// Paste a freshly generated device cert → Parse Cert auto-fills SFDI/LFDI →
// fill PIN + description → click Add Device → device appears in the list.
//
// We auth via Bearer (the e2e harness's standard path) so this exercises
// the new /api/certs/info, /api/devices, and /api/devices/by-lfdi endpoints
// behind AdminAuthMiddleware without depending on the cookie flow. The
// cookie flow is unit-tested in internal/auth and internal/server.

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer();
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders({
    'Authorization': 'Bearer e2e-test-key',
  });
});

test('add device from pasted certificate', async ({ page }) => {
  // Generate a fresh device cert (the sep2server binary built by setup.ts).
  const certDir = getCertDir();
  const projectRoot = join(__dirname, '..');
  const sep2server = join(projectRoot, 'sep2server');

  execFileSync(sep2server, [
    'certs', 'generate-device',
    '--ca', join(certDir, 'ca.crt'),
    '--ca-key', join(certDir, 'ca.key'),
    '--hw-serial', 'IEEE-095-E2E',
    '--hw-type', '1.3.6.1.4.1.40732.99',
    '--out', certDir,
    '--name', 'e2e-test-device',
  ], { stdio: 'pipe' });
  const devicePEM = readFileSync(join(certDir, 'e2e-test-device.crt'), 'utf8');

  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  // 1) Paste cert.
  await page.locator('#addDevCert').fill(devicePEM);

  // 2) Parse → SFDI/LFDI auto-fill.
  await page.getByRole('button', { name: 'Parse Cert' }).click();
  await expect(page.locator('#addDevSFDI')).not.toHaveValue('', { timeout: 5000 });
  await expect(page.locator('#addDevLFDI')).not.toHaveValue('');
  const sfdi = await page.locator('#addDevSFDI').inputValue();
  const lfdi = await page.locator('#addDevLFDI').inputValue();
  expect(sfdi).toMatch(/^\d{12}$/);
  expect(lfdi).toMatch(/^[0-9A-Fa-f]{40}$/);

  // 3) Fill description + PIN, leave Enabled checked.
  await page.locator('#addDevDesc').fill('IEEE-095 E2E test device');
  await page.locator('#addDevPIN').fill('424242');

  // 4) Submit.
  await page.getByRole('button', { name: 'Add Device' }).click();
  await expect(page.locator('#addDevResult')).toContainText(/Created \/edev\//, { timeout: 5000 });

  // 5) Verify lookup-by-LFDI returns the new device.
  await page.locator('#lookupLFDI').fill(lfdi);
  await page.getByRole('button', { name: 'Lookup' }).click();
  await expect(page.locator('#lookupResult')).toContainText(`SFDI=${sfdi}`, { timeout: 5000 });
});

test('lookup unknown LFDI returns found:false', async ({ page }) => {
  await page.goto(baseUrl + '/?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  // 40 hex chars that won't match anything.
  await page.locator('#lookupLFDI').fill('00000000000000000000000000000000DEADBEEF');
  await page.getByRole('button', { name: 'Lookup' }).click();
  await expect(page.locator('#lookupResult')).toContainText('No device registered', { timeout: 5000 });
});
