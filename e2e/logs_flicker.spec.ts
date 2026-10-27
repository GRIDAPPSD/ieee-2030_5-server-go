import { test, expect, type Page } from '@playwright/test';
import { mkdtempSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert } from './fixtures/device_cert';
import { readWire } from './fixtures/fsa_wire';

// #1135: the dashboard pushes a frame every 5 s, and a tab with nothing
// changing must not replace a single element for it. Elements are tagged in
// the page, a few frames are let through, and any element that lost its tag
// or arrived without one was replaced by a frame.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const SERVER = join(__dirname, '..', 'sep2server');
const PROTOCOL_PORT = 23000 + Math.floor(Math.random() * 1000);
const FRAMES = 3;

let baseUrl: string;
let lfdi: string;

test.beforeAll(async ({ request }) => {
  baseUrl = await startServer({
    SEP2_ADDR: `:${PROTOCOL_PORT}`,
    SEP2_PEN: '12345',
    SEP2_DEVICE_LOG_DIR: mkdtempSync(join(tmpdir(), 'sep2-e2e-flicker-')),
  });
  await expect
    .poll(async () => (await request.get(`${baseUrl}/api/fsas`, { headers: AUTH }).catch(() => null))?.status(), { timeout: 15000 })
    .toBe(200);
  const cert = mintDeviceCert(SERVER, getCertDir(), 'logs-flicker');
  const info = await request.post(`${baseUrl}/api/certs/info`, {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: cert.certPEM,
  });
  expect(info.status()).toBe(200);
  const body = await info.json();
  lfdi = body.lfdi;
  const added = await request.post(`${baseUrl}/api/devices`, {
    headers: AUTH,
    data: { sfdi: body.sfdi, lfdi, name: 'logs-flicker', pin: 424244, enabled: true },
  });
  expect(added.status()).toBe(201);
  for (let i = 0; i < 5; i++) {
    expect((await readWire(PROTOCOL_PORT, getCertDir(), cert, '/dcap')).status).toBe(200);
  }
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders(AUTH);
  // Counts the dashboard frames the page receives, so the test waits for
  // frames and not for a guessed number of seconds.
  await page.addInitScript(() => {
    const w = window as unknown as { __frames: number };
    w.__frames = 0;
    const Native = window.EventSource;
    window.EventSource = class extends Native {
      constructor(url: string | URL, init?: EventSourceInit) {
        super(url, init);
        if (String(url).includes('/dashboard/events')) this.addEventListener('message', () => (w.__frames += 1));
      }
    } as typeof EventSource;
  });
});

// Tags every element under the page grid, waits for FRAMES more frames, and
// returns a description of each element that was replaced.
async function replacedAcrossFrames(page: Page, scope: string): Promise<string[]> {
  const seen = (): Promise<number> => page.evaluate(() => (window as unknown as { __frames: number }).__frames);
  const start = await seen();
  await page.evaluate((sel) => {
    const root = document.querySelector(sel)!;
    let n = 0;
    for (const el of [root, ...root.querySelectorAll('*')]) (el as unknown as { __tag: number }).__tag = ++n;
  }, scope);
  await expect.poll(seen, { timeout: 40000, intervals: [500] }).toBeGreaterThanOrEqual(start + FRAMES);
  return page.evaluate((sel) => {
    const root = document.querySelector(sel)!;
    const lost: string[] = [];
    for (const el of [root, ...root.querySelectorAll('*')]) {
      if ((el as unknown as { __tag?: number }).__tag === undefined) {
        lost.push(`${el.tagName.toLowerCase()}${el.getAttribute('data-testid') ? `[${el.getAttribute('data-testid')}]` : ''}`);
      }
    }
    return lost;
  }, scope);
}

test('the Logs tab replaces no element when dashboard frames arrive', async ({ page }) => {
  await page.goto(baseUrl + `/ui/logs?device=${lfdi}&token=${ADMIN_KEY}`);
  const logs = page.getByTestId('device-logs');
  await expect(logs.getByRole('row').filter({ hasText: 'GET /dcap' }).first()).toBeVisible({ timeout: 15000 });
  // Control: the table really has rows to be replaced.
  expect(await logs.getByTestId('device-log-row').count()).toBeGreaterThan(0);

  expect(await replacedAcrossFrames(page, 'body')).toEqual([]);
});

test('the Devices tab replaces no element when dashboard frames arrive', async ({ page }) => {
  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await expect(page.getByTestId('tab-devices')).toHaveAttribute('aria-current', 'page');
  await expect(page.locator('#deviceTable')).toBeVisible();
  // Control: the table lists the device whose frames are being counted.
  await expect(page.locator('#deviceTable')).toContainText('logs-flicker');

  expect(await replacedAcrossFrames(page, 'body')).toEqual([]);
});
