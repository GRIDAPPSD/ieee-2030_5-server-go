import { test, expect, type Page } from '@playwright/test';
import { execFileSync } from 'child_process';
import { join } from 'path';
import { readFileSync } from 'fs';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';

// #929: the operator's whole flow from an empty server, UI only. Every write
// below is a click or a keystroke in the admin UI; the API is read for
// assertions and never written. The server boots with no boot fixture, so
// there is no device, FSA or program until the UI makes one.

const KEY = ADMIN_KEY;
const AUTH = { Authorization: `Bearer ${KEY}` };
const FSA_MRID = 'e2e-runtime-fsa';
const TEMPLATE = 'runtime template';

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer({ SEP2_PEN: '12345' });
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders(AUTH);
});

function mintCert(name: string): string {
  const certDir = getCertDir();
  execFileSync(join(__dirname, '..', 'sep2server'), [
    'certs', 'generate-device',
    '--ca', join(certDir, 'ca.crt'),
    '--ca-key', join(certDir, 'ca.key'),
    '--hw-serial', `${name}-serial`,
    '--hw-type', '1.3.6.1.4.1.40732.99',
    '--out', certDir,
    '--name', name,
  ], { stdio: 'pipe' });
  return readFileSync(join(certDir, `${name}.crt`), 'utf8');
}

// addDeviceInUI pastes a certificate into the Add End Device card and
// returns the identifiers the panel parsed out of it.
async function addDeviceInUI(page: Page, name: string): Promise<{ sfdi: string; lfdi: string }> {
  await page.locator('#addDevCert').fill(mintCert(name));
  await page.getByRole('button', { name: 'Parse Cert' }).click();
  // The readonly fields keep the previous device's values until the parse
  // response lands, so a non-empty field proves nothing on the second call.
  // The result line is set in the same tick as both fields.
  await expect(page.locator('#addDevResult')).toContainText(/Parsed cert\./, { timeout: 5000 });
  const sfdi = await page.locator('#addDevSFDI').inputValue();
  const lfdi = await page.locator('#addDevLFDI').inputValue();
  await page.locator('#addDevDesc').fill(name);
  await page.locator('#addDevPIN').fill('424242');
  await page.getByRole('button', { name: 'Add Device' }).click();
  await expect(page.locator('#addDevResult')).toContainText(/Created \/edev\//, { timeout: 5000 });
  await expect(page.getByTestId('device-sfdi').filter({ hasText: sfdi })).toHaveCount(1, { timeout: 5000 });
  return { sfdi, lfdi };
}

// localInput formats a Date the way a datetime-local input takes it.
function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

test('an empty server becomes controllable from the UI alone', async ({ page, request }) => {
  // The admin listener can accept a TCP connection before it serves (#952),
  // so wait on a real read before the first UI action.
  await expect
    .poll(async () => (await request.get(`${baseUrl}/api/fsas`, { headers: AUTH }).catch(() => null))?.status(), {
      timeout: 15000,
    })
    .toBe(200);

  // Control: the server really is empty.
  const emptyFsas = await request.get(`${baseUrl}/api/fsas`, { headers: AUTH });
  expect(emptyFsas.status()).toBe(200);
  expect(JSON.stringify(await emptyFsas.json())).not.toContain(FSA_MRID);

  // 1) Add an aggregator and a managed device.
  await page.goto(`${baseUrl}/ui/devices?token=${KEY}`);
  const aggregator = await addDeviceInUI(page, 'runtime-aggregator');
  const managed = await addDeviceInUI(page, 'runtime-managed');
  for (const d of [aggregator, managed]) {
    const found = await request.get(`${baseUrl}/api/devices/by-lfdi/${d.lfdi}`, { headers: AUTH });
    expect(found.status()).toBe(200);
    expect(JSON.stringify(await found.json())).toContain(d.sfdi);
  }

  // 2) Pair them from the Devices tab.
  const manager = page.getByLabel('Manager', { exact: true });
  const managedSel = page.getByLabel('Managed device', { exact: true });
  await expect(manager.locator('option', { hasText: aggregator.sfdi })).toHaveCount(1, { timeout: 10000 });
  await manager.selectOption({ value: (await manager.locator('option', { hasText: aggregator.sfdi }).getAttribute('value'))! });
  await managedSel.selectOption({ value: (await managedSel.locator('option', { hasText: managed.sfdi }).getAttribute('value'))! });
  await page.getByRole('button', { name: 'Create pair' }).click();
  await expect(page.getByTestId('pairs-result')).toContainText('Paired', { timeout: 5000 });
  const pair = await request.get(
    `${baseUrl}/api/management-pairs?manager=${aggregator.lfdi.toUpperCase()}`,
    { headers: AUTH },
  );
  expect((await pair.json()).managedLFDIs).toEqual([managed.lfdi.toUpperCase()]);

  // 3) Create an FSA, then a program template on it, from the FSAs tab.
  await page.getByTestId('tab-fsas').click();
  await page.locator('#newFSADesc').fill('runtime FSA');
  await page.locator('#newFSAMRID').fill(FSA_MRID);
  await page.locator('#newFSAPrimacy').fill('1');
  await page.getByRole('button', { name: 'Create FSA' }).click();
  await expect(page.locator('#createFSAResult')).toContainText(FSA_MRID, { timeout: 5000 });

  const templates = page.getByTestId(`fsa-templates-${FSA_MRID}`);
  await templates.getByRole('button', { name: 'New template' }).click();
  await templates.getByLabel('Description').fill(TEMPLATE);
  await templates.getByLabel('Primacy').fill('1');
  await templates.getByLabel('Default control').selectOption('connect');
  await templates.getByRole('button', { name: 'Add template' }).click();
  await expect(templates.getByTestId('template-description')).toHaveText(TEMPLATE, { timeout: 5000 });

  // 4) Assign the managed device to the FSA from the Devices tab.
  await page.getByTestId('tab-devices').click();
  const row = page.getByRole('row').filter({ has: page.getByTestId('device-sfdi').filter({ hasText: managed.sfdi }) });
  await row.getByRole('combobox').selectOption({ value: `/api/fsas/${FSA_MRID}` });
  await row.getByRole('button', { name: 'Assign' }).click();
  await expect(page.getByTestId('device-table-error')).toHaveCount(0);
  await expect
    .poll(async () => ((await (await request.get(`${baseUrl}/api/fsas/${FSA_MRID}`, { headers: AUTH })).json()).devices ?? []).length, {
      timeout: 5000,
    })
    .toBe(1);

  // 5) Send a control scheduled an hour ahead from the Control tab.
  await page.getByTestId('tab-control').click();
  await page.locator('#controlDevice').selectOption({ label: managed.sfdi });
  await page.locator('#controlProgram').selectOption({ label: TEMPLATE });
  await page.getByTestId('control-fn-opModConnect').check();
  await page.getByTestId('control-fv-opModConnect').check();
  await page.locator('#controlStartNow').uncheck();
  await page.locator('#controlStartAt').fill(localInput(new Date(Date.now() + 3600 * 1000)));
  await page.locator('#controlDuration').fill('30');
  await page.getByRole('button', { name: 'Send' }).click();
  await page.getByRole('button', { name: 'Confirm' }).click();

  const result = page.locator('#controlResult');
  await expect(result).toContainText(/[0-9A-F]{32}/, { timeout: 5000 });
  const mrid = (await result.textContent())?.match(/[0-9A-F]{32}/)?.[0];
  expect(mrid).toBeTruthy();

  const controlRow = page.locator(`[data-testid="der-control-row"][data-mrid="${mrid}"]`);
  await expect(controlRow.getByTestId('der-control-status')).toHaveText('scheduled', { timeout: 5000 });
});
