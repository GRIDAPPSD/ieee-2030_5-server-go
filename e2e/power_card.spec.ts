import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert, type DeviceCert } from './fixtures/device_cert';
import { sendWire } from './fixtures/fsa_wire';

// #1115: the Overview power card. A device posts real power over mutual TLS,
// discharging and then charging, and the card must show both as the power
// route reports them, plus the power now.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const SERVER = join(__dirname, '..', 'sep2server');
// Outside setup.ts's 18443-20442 range and the ranges of the other specs.
const PROTOCOL_PORT = 25000 + Math.floor(Math.random() * 1000);
const NS = 'urn:ieee:std:2030.5:ns';
const DISCHARGE_W = 4000;
const CHARGE_W = 1500;

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer({ SEP2_ADDR: `:${PROTOCOL_PORT}`, SEP2_PEN: '12345' });
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders(AUTH);
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function postPower(cert: DeviceCert, mupPath: string, watts: number, flowDirection: number, n: number) {
  const body =
    `<MirrorMeterReading xmlns="${NS}"><mRID>W${n}</mRID>` +
    `<ReadingType><uom>38</uom><flowDirection>${flowDirection}</flowDirection></ReadingType>` +
    `<Reading><value>${watts}</value></Reading></MirrorMeterReading>`;
  const r = await sendWire(PROTOCOL_PORT, getCertDir(), cert, 'POST', mupPath, body);
  expect([200, 201]).toContain(r.status);
}

test('the Overview power card shows a seeded device\'s discharged and charged energy and its power now', async ({ page, request }) => {
  test.setTimeout(60000);
  await expect
    .poll(async () => (await request.get(`${baseUrl}/api/fsas`, { headers: AUTH }).catch(() => null))?.status(), { timeout: 15000 })
    .toBe(200);

  const cert = mintDeviceCert(SERVER, getCertDir(), 'power-card');
  const info = await request.post(`${baseUrl}/api/certs/info`, {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: cert.certPEM,
  });
  expect(info.status()).toBe(200);
  const { sfdi, lfdi } = await info.json();
  const added = await request.post(`${baseUrl}/api/devices`, {
    headers: AUTH,
    data: { sfdi, lfdi, name: 'power-card', pin: 515151, enabled: true },
  });
  expect(added.status()).toBe(201);

  // Control: before the device posts, the route has no energy for it.
  const before = await request.get(`${baseUrl}/api/derms/power?window=900&scope=all`, { headers: AUTH });
  expect(before.status()).toBe(200);
  expect((await before.json()).latestW).toBeNull();

  const mup =
    `<MirrorUsagePoint xmlns="${NS}"><mRID>0A0A0A0A</mRID><deviceLFDI>${lfdi}</deviceLFDI>` +
    `<roleFlags>08</roleFlags><serviceCategoryKind>0</serviceCategoryKind><status>1</status>` +
    `<postRate>2</postRate></MirrorUsagePoint>`;
  const created = await sendWire(PROTOCOL_PORT, getCertDir(), cert, 'POST', '/mup', mup);
  expect([200, 201]).toContain(created.status);
  const mupPath = created.location;
  expect(mupPath).toMatch(/^\/mup\/\w+/);

  // Discharge, then charge, a reading a second so every 5 s bucket has one.
  // Reverse (19) is export on a DER; Forward (1) is import.
  let n = 0;
  for (let i = 0; i < 7; i++) {
    await postPower(cert, mupPath, DISCHARGE_W, 19, n++);
    await sleep(1000);
  }
  for (let i = 0; i < 7; i++) {
    await postPower(cert, mupPath, CHARGE_W, 1, n++);
    await sleep(1000);
  }

  await page.goto(baseUrl + `/ui/overview?token=${ADMIN_KEY}`);
  const card = page.getByTestId('power-card');
  await expect(card).toBeVisible();
  await expect(card.getByTestId('descriptor-chart')).toBeVisible({ timeout: 15000 });
  await expect(card.getByTestId('power-now')).toHaveText('-1.5 kW', { timeout: 15000 });

  // The chart draws on a canvas, so the sums the card prints under it are
  // what the page shows of the two series.
  const totals = card.getByTestId('power-energy-totals');
  await expect(totals).toHaveText(/discharged (\d+\.\d+) kWh, charged (\d+\.\d+) kWh/);
  const [, discharged, charged] = (await totals.innerText()).match(/discharged (\S+) kWh, charged (\S+) kWh/)!;
  expect(Number(discharged)).toBeGreaterThan(0);
  expect(Number(charged)).toBeGreaterThan(0);

  // Leaving the tab stops the polling, and a return draws the kept chart at
  // once, marked with its time, while the next read has not answered.
  let reads = 0;
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route('**/api/derms/power**', async (route) => {
    reads++;
    await held;
    await route.abort();
  });
  await page.getByTestId('tab-devices').click();
  await expect(card).toHaveCount(0);
  await page.waitForTimeout(7000);
  expect(reads).toBe(0);

  await page.getByTestId('tab-overview').click();
  await expect(page.getByTestId('power-card').getByTestId('descriptor-chart')).toBeVisible();
  await expect(page.getByTestId('power-stale')).toContainText('Showing the answer from');
  await expect(page.getByTestId('power-now')).toHaveText('-1.5 kW');
  expect(reads).toBe(1);
  release();
});
