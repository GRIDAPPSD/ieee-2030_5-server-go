import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert } from './fixtures/device_cert';

// #926, #1035: add an aggregator from the Devices tab, picking its devices,
// then read the pairs back through GET /api/management-pairs, the endpoint a
// device's protocol reads are decided from. The panel is asserted through
// that API rather than its own table, so a panel that showed a pair it never
// stored would fail here.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };

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

// registerDevice mints a device certificate and registers it through the
// same endpoints the Add End Device panel uses, returning the identifiers
// the server derived from the certificate and the PEM it minted.
async function registerDevice(
  request: APIRequestContext,
  name: string,
  kind = 'device',
): Promise<{ sfdi: string; lfdi: string }> {
  const { sfdi, lfdi } = await deriveIdentity(request, mintPEM(name));
  const added = await request.post(baseUrl + '/api/devices', {
    headers: AUTH,
    data: { sfdi, lfdi, name, kind, pin: 424242, enabled: true },
  });
  expect(added.status()).toBe(201);
  return { sfdi, lfdi };
}

function mintPEM(name: string): string {
  return mintDeviceCert(join(__dirname, '..', 'sep2server'), getCertDir(), name).certPEM;
}

async function deriveIdentity(request: APIRequestContext, pem: string): Promise<{ sfdi: string; lfdi: string }> {
  const info = await request.post(baseUrl + '/api/certs/info', {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: pem,
  });
  expect(info.ok()).toBe(true);
  return info.json();
}

async function pairsOf(request: APIRequestContext, managerLFDI: string): Promise<string[]> {
  const res = await request.get(`${baseUrl}/api/management-pairs?manager=${managerLFDI}`, { headers: AUTH });
  expect(res.status()).toBe(200);
  return (await res.json()).managedLFDIs ?? [];
}

async function fillAddForm(page: Page, pem: string, name: string) {
  await page.locator('#addDevCert').fill(pem);
  await page.getByRole('button', { name: 'Parse Cert' }).click();
  await expect(page.locator('#addDevLFDI')).not.toHaveValue('', { timeout: 5000 });
  await page.locator('#addDevDesc').fill(name);
  await page.locator('#addDevPIN').fill('424242');
  await page.locator('#addDevKind').selectOption('aggregator');
}

test('add an aggregator with two devices and list both pairs, then remove one', async ({ page, request }) => {
  const devA = await registerDevice(request, 'agg-dev-a');
  const devB = await registerDevice(request, 'agg-dev-b');
  const pem = mintPEM('agg-new');
  const agg = await deriveIdentity(request, pem);
  const aggLFDI = agg.lfdi.toUpperCase();

  // Control: the aggregator does not exist and holds nothing before the form acts.
  expect(await pairsOf(request, aggLFDI)).toEqual([]);

  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await page.waitForLoadState('domcontentloaded');
  await fillAddForm(page, pem, 'agg-new');

  const picker = page.getByTestId('device-picker');
  await expect(picker.getByRole('checkbox', { name: 'agg-dev-a' })).toBeVisible({ timeout: 10000 });
  await picker.getByRole('checkbox', { name: 'agg-dev-a' }).check();
  await picker.getByRole('checkbox', { name: 'agg-dev-b' }).check();
  await page.getByRole('button', { name: 'Add Device' }).click();

  await expect(page.locator('#addDevResult')).toContainText('Paired 2 of 2 devices.', { timeout: 10000 });
  expect((await pairsOf(request, aggLFDI)).sort()).toEqual([devA.lfdi.toUpperCase(), devB.lfdi.toUpperCase()].sort());

  // Both pairs are listed on the pairs screen, and removal clears one.
  const rowA = page.getByTestId('pair-row').filter({ hasText: devA.lfdi.toUpperCase() });
  await expect(rowA).toHaveCount(1, { timeout: 10000 });
  await expect(page.getByTestId('pair-row').filter({ hasText: devB.lfdi.toUpperCase() })).toHaveCount(1);
  await rowA.getByRole('button', { name: 'Remove' }).click();
  await expect(page.getByTestId('pairs-result')).toContainText('Removed', { timeout: 5000 });
  expect(await pairsOf(request, aggLFDI)).toEqual([devB.lfdi.toUpperCase()]);
});

test('the picker excludes aggregators and devices managed elsewhere', async ({ page, request }) => {
  const other = await registerDevice(request, 'pick-other-agg', 'aggregator');
  const held = await registerDevice(request, 'pick-held');
  await registerDevice(request, 'pick-free');
  const pair = await request.post(baseUrl + '/api/management-pairs', {
    headers: AUTH,
    data: { managerLFDI: other.lfdi.toUpperCase(), managedLFDI: held.lfdi.toUpperCase() },
  });
  expect(pair.status()).toBeLessThan(300);

  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await page.waitForLoadState('domcontentloaded');
  await page.locator('#addDevKind').selectOption('aggregator');

  const picker = page.getByTestId('device-picker');
  await expect(picker.getByRole('checkbox', { name: 'pick-free' })).toBeVisible({ timeout: 10000 });
  await expect(picker.getByRole('checkbox', { name: 'pick-held' })).toHaveCount(0);
  await expect(picker.getByRole('checkbox', { name: 'pick-other-agg' })).toHaveCount(0);
});

test('a refused pair is named by its device and the aggregator stays', async ({ page, request }) => {
  const devA = await registerDevice(request, 'fail-dev-a');
  await registerDevice(request, 'fail-dev-b');
  const pem = mintPEM('fail-agg');
  const agg = await deriveIdentity(request, pem);
  const aggLFDI = agg.lfdi.toUpperCase();

  // The server's own refusal for a device held elsewhere cannot be reached
  // through a picker that already hides such devices, so the pair POST for one
  // device is answered with the server's 409 text.
  await page.route('**/api/management-pairs', async (route) => {
    if (route.request().method() === 'POST' && route.request().postData()?.includes(devA.lfdi.toUpperCase())) {
      await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'device already has a different manager' }) });
      return;
    }
    await route.continue();
  });

  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await page.waitForLoadState('domcontentloaded');
  await fillAddForm(page, pem, 'fail-agg');
  const picker = page.getByTestId('device-picker');
  await expect(picker.getByRole('checkbox', { name: 'fail-dev-a' })).toBeVisible({ timeout: 10000 });
  await picker.getByRole('checkbox', { name: 'fail-dev-a' }).check();
  await picker.getByRole('checkbox', { name: 'fail-dev-b' }).check();
  await page.getByRole('button', { name: 'Add Device' }).click();

  const result = page.locator('#addDevResult');
  await expect(result).toContainText('Pair failed for fail-dev-a', { timeout: 10000 });
  await expect(result).toContainText('Paired 1 of 2 devices.');
  expect(await pairsOf(request, aggLFDI)).toHaveLength(1);
  const lookup = await request.get(`${baseUrl}/api/devices/by-lfdi/${aggLFDI}`, { headers: AUTH });
  expect(lookup.status()).toBe(200);
});
