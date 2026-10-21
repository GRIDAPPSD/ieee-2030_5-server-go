import { test, expect, type APIRequestContext } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert } from './fixtures/device_cert';

// #926: pair an aggregator with a managed device from the Devices tab, then
// read the pair back through GET /api/management-pairs, the endpoint a
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
// the server derived from the certificate.
async function registerDevice(
  request: APIRequestContext,
  name: string,
): Promise<{ sfdi: string; lfdi: string }> {
  const certDir = getCertDir();
  const pem = mintDeviceCert(join(__dirname, '..', 'sep2server'), certDir, name).certPEM;

  const info = await request.post(baseUrl + '/api/certs/info', {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: pem,
  });
  expect(info.ok()).toBe(true);
  const { sfdi, lfdi } = await info.json();

  const added = await request.post(baseUrl + '/api/devices', {
    headers: AUTH,
    data: { sfdi, lfdi, description: name, pin: 424242, enabled: true },
  });
  expect(added.status()).toBe(201);
  return { sfdi, lfdi };
}

test('create and remove a management pair from the Devices tab', async ({ page, request }) => {
  const aggregator = await registerDevice(request, 'pairs-aggregator');
  const managed = await registerDevice(request, 'pairs-managed');
  const managerLFDI = aggregator.lfdi.toUpperCase();
  const managedLFDI = managed.lfdi.toUpperCase();

  // Control: nothing is paired before the panel acts.
  const before = await request.get(`${baseUrl}/api/management-pairs?manager=${managerLFDI}`, { headers: AUTH });
  expect(before.status()).toBe(200);
  expect((await before.json()).managedLFDIs ?? []).toEqual([]);

  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await page.waitForLoadState('domcontentloaded');

  // Both choices come from the device list; the option value is the LFDI.
  const manager = page.getByLabel('Manager', { exact: true });
  const managedSel = page.getByLabel('Managed device', { exact: true });
  await expect(manager.locator('option', { hasText: aggregator.sfdi })).toHaveCount(1, { timeout: 10000 });
  await manager.selectOption({ value: (await manager.locator('option', { hasText: aggregator.sfdi }).getAttribute('value'))! });
  await managedSel.selectOption({ value: (await managedSel.locator('option', { hasText: managed.sfdi }).getAttribute('value'))! });
  await page.getByRole('button', { name: 'Create pair' }).click();

  await expect(page.getByTestId('pairs-result')).toContainText('Paired', { timeout: 5000 });
  const created = await request.get(`${baseUrl}/api/management-pairs?manager=${managerLFDI}`, { headers: AUTH });
  expect(created.status()).toBe(200);
  expect((await created.json()).managedLFDIs).toEqual([managedLFDI]);
  const byManaged = await request.get(`${baseUrl}/api/management-pairs?managed=${managedLFDI}`, { headers: AUTH });
  expect((await byManaged.json()).managerLFDI).toBe(managerLFDI);

  // The pair is listed, and removing it clears it from the server.
  const row = page.getByTestId('pair-row').filter({ hasText: managedLFDI });
  await expect(row).toHaveCount(1);
  await row.getByRole('button', { name: 'Remove' }).click();
  await expect(page.getByTestId('pairs-result')).toContainText('Removed', { timeout: 5000 });
  const gone = await request.get(`${baseUrl}/api/management-pairs?managed=${managedLFDI}`, { headers: AUTH });
  expect(gone.status()).toBe(404);
});
