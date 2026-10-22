import { test, expect } from '@playwright/test';
import { mkdtempSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert } from './fixtures/device_cert';
import { readWire } from './fixtures/fsa_wire';

// #1067: the Logs drawer of a device. The device makes real requests over
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

test('the Logs drawer lists the requests a device made, with a Download link', async ({ page, request }) => {
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
  await page.getByRole('button', { name: `Logs of device ${id}` }).click();
  const drawer = page.getByTestId(`device-logs-${id}`);
  await expect(drawer.getByRole('row').filter({ hasText: 'GET /dcap' })).toBeVisible({ timeout: 15000 });
  await expect(drawer.getByRole('row').filter({ hasText: 'GET /tm' })).toBeVisible();
  await expect(drawer.getByTestId('device-logs-download')).toHaveAttribute('href', `/api/device-logs/${lfdi}/download`);

  await drawer.getByRole('button', { name: 'Close logs' }).click();
  await expect(drawer).toHaveCount(0);
});
