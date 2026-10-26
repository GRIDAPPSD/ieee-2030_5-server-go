import { test, expect } from '@playwright/test';
import { mkdtempSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert } from './fixtures/device_cert';
import { readWire } from './fixtures/fsa_wire';

// #1067, #1083: the Logs tab of a device. The device makes real requests over
// mutual TLS on the protocol port; the drawer must show them as the log
// writer recorded them, read through GET /api/device-logs/{lfdi}.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const SERVER = join(__dirname, '..', 'sep2server');
// Outside setup.ts's 18443-20442 range and the 21000 and 22000 ranges of
// fsa_control.spec.ts and fsa_unassign.spec.ts.
const PROTOCOL_PORT = 23000 + Math.floor(Math.random() * 1000);

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer({
    SEP2_ADDR: `:${PROTOCOL_PORT}`,
    SEP2_PEN: '12345',
    SEP2_DEVICE_LOG_DIR: mkdtempSync(join(tmpdir(), 'sep2-e2e-devlog-')),
  });
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders(AUTH);
});

test('the Logs tab lists the requests a device made, survives a reload, and links to Download', async ({ page, request }) => {
  await expect
    .poll(async () => (await request.get(`${baseUrl}/api/fsas`, { headers: AUTH }).catch(() => null))?.status(), { timeout: 15000 })
    .toBe(200);

  const cert = mintDeviceCert(SERVER, getCertDir(), 'device-log-ui');
  const info = await request.post(`${baseUrl}/api/certs/info`, {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: cert.certPEM,
  });
  expect(info.status()).toBe(200);
  const { sfdi, lfdi } = await info.json();
  const added = await request.post(`${baseUrl}/api/devices`, {
    headers: AUTH,
    data: { sfdi, lfdi, name: 'device-log-ui', pin: 424242, enabled: true },
  });
  expect(added.status()).toBe(201);
  const id = String((await added.json()).href).split('/').pop()!;

  // Control: before the device speaks, the API has no log for it.
  const before = await request.get(`${baseUrl}/api/device-logs/${lfdi}`, { headers: AUTH });
  expect(before.status()).toBe(404);

  expect((await readWire(PROTOCOL_PORT, getCertDir(), cert, '/dcap')).status).toBe(200);
  expect((await readWire(PROTOCOL_PORT, getCertDir(), cert, '/tm')).status).toBe(200);

  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await page.getByTestId(`device-logs-link-${id}`).click();
  await expect(page).toHaveURL(new RegExp(`/ui/logs\\?device=${lfdi}$`));
  const logs = page.getByTestId('device-logs');
  await expect(logs.getByRole('row').filter({ hasText: 'GET /dcap' })).toBeVisible({ timeout: 15000 });
  await expect(logs.getByRole('row').filter({ hasText: 'GET /tm' })).toBeVisible();
  // The lines sit in their own scroll box, and a short log says where it starts
  // instead of offering an Older button.
  await expect(logs.getByTestId('device-logs-scroll')).toBeVisible();
  await expect(logs.getByTestId('device-logs-start')).toBeVisible();
  await expect(logs.getByTestId('device-logs-older')).toHaveCount(0);
  await expect(logs.getByTestId('device-logs-download')).toHaveAttribute('href', `/api/device-logs/${lfdi}/download`);

  await page.reload();
  await expect(page.getByTestId('logs-picker')).toHaveValue(lfdi);
  await expect(page.getByTestId('device-logs').getByRole('row').filter({ hasText: 'GET /dcap' })).toBeVisible({ timeout: 15000 });

  await page.goto(baseUrl + '/ui/logs?device=0000000000000000000000000000000000000000');
  await expect(page.getByTestId('logs-not-found')).toBeVisible();
  await expect(page.getByTestId('device-logs')).toHaveCount(0);
});

// #1129: the log reads like a tail. Opens at the bottom, a new line while the
// reader is scrolled up waits behind a control, and the control jumps down.
test('the Logs tab opens at the bottom and holds new lines while the reader is scrolled up', async ({ page, request }) => {
  const cert = mintDeviceCert(SERVER, getCertDir(), 'device-log-tail');
  const info = await request.post(`${baseUrl}/api/certs/info`, {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: cert.certPEM,
  });
  expect(info.status()).toBe(200);
  const { sfdi, lfdi } = await info.json();
  const added = await request.post(`${baseUrl}/api/devices`, {
    headers: AUTH,
    data: { sfdi, lfdi, name: 'device-log-tail', pin: 424243, enabled: true },
  });
  expect(added.status()).toBe(201);

  // Enough lines that the box scrolls; the last one is distinguishable.
  for (let i = 0; i < 40; i++) {
    expect((await readWire(PROTOCOL_PORT, getCertDir(), cert, '/dcap')).status).toBe(200);
  }
  expect((await readWire(PROTOCOL_PORT, getCertDir(), cert, '/tm')).status).toBe(200);

  await page.goto(baseUrl + `/ui/logs?device=${lfdi}&token=${ADMIN_KEY}`);
  const logs = page.getByTestId('device-logs');
  const box = logs.getByTestId('device-logs-scroll');
  const metrics = () =>
    box.evaluate((el) => ({ top: el.scrollTop, gap: el.scrollHeight - el.scrollTop - el.clientHeight, scrollable: el.scrollHeight > el.clientHeight }));

  await expect(logs.getByRole('row').filter({ hasText: 'GET /tm' })).toBeVisible({ timeout: 15000 });
  // Control: the box really scrolls, so being at the bottom is not vacuous.
  expect((await metrics()).scrollable).toBe(true);
  await expect.poll(async () => (await metrics()).gap).toBeLessThan(8);
  const rows = logs.getByTestId('device-log-row');
  await expect(rows.last()).toContainText('GET /tm');

  await box.evaluate((el) => {
    el.scrollTop = 0;
  });
  await expect.poll(async () => (await metrics()).top).toBe(0);
  expect((await readWire(PROTOCOL_PORT, getCertDir(), cert, '/edev')).status).toBeGreaterThan(0);

  const jump = logs.getByTestId('device-logs-new');
  await expect(jump).toBeVisible({ timeout: 15000 });
  expect((await metrics()).top).toBe(0);

  await jump.click();
  await expect.poll(async () => (await metrics()).gap).toBeLessThan(8);
  await expect(rows.last()).toContainText('GET /edev');
  await expect(jump).toHaveCount(0);
});
