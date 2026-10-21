import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert, type DeviceCert } from './fixtures/device_cert';
import { readWire } from './fixtures/fsa_wire';

// #973: a control sent to an FSA's program from the Control tab reaches every
// device assigned to the FSA. The devices need certificates the server's CA
// signed, so they cannot come from a boot fixture; they and the FSA, its
// program and the assignments are seeded through the admin API, and the
// control itself is sent and cancelled only through the UI. The wire is read
// as each device, over mutual TLS on the protocol port.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const FSA_MRID = 'e2e-fsa-control';
const FSA_LABEL = 'fsa control FSA';
const TEMPLATE = 'fsa control program';
const SERVER = join(__dirname, '..', 'sep2server');
// Outside the 18443-20442 range setup.ts picks its own ports from.
const PROTOCOL_PORT = 21000 + Math.floor(Math.random() * 1000);

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

function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

test('an FSA control from the Control tab is on both devices\' wire lists, and cancel reaches both', async ({ page, request }) => {
  await expect
    .poll(async () => (await request.get(`${baseUrl}/api/fsas`, { headers: AUTH }).catch(() => null))?.status(), { timeout: 15000 })
    .toBe(200);

  // Seed: two devices with real certificates, one FSA, one program on it,
  // and both devices assigned.
  const devices: { cert: DeviceCert; id: string }[] = [];
  for (const name of ['fsa-control-a', 'fsa-control-b']) {
    const cert = mintDeviceCert(SERVER, getCertDir(), name);
    const info = await request.post(`${baseUrl}/api/certs/info`, {
      headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
      data: cert.certPEM,
    });
    expect(info.status()).toBe(200);
    const { sfdi, lfdi } = await info.json();
    const added = await request.post(`${baseUrl}/api/devices`, {
      headers: AUTH,
      data: { sfdi, lfdi, name, pin: 424242, enabled: true },
    });
    expect(added.status()).toBe(201);
    devices.push({ cert, id: String((await added.json()).href).split('/').pop()! });
  }
  const fsa = await request.post(`${baseUrl}/api/fsas`, {
    headers: AUTH,
    data: { description: FSA_LABEL, mRID: FSA_MRID, primacy: 1 },
  });
  expect(fsa.status()).toBe(201);
  const template = await request.post(`${baseUrl}/api/fsas/${FSA_MRID}/der-programs`, {
    headers: AUTH,
    data: { description: TEMPLATE, primacy: 1, defaultControl: { type: 'connect' } },
  });
  expect(template.status()).toBe(201);
  const templateId: string = (await template.json()).id;
  for (const d of devices) {
    const assigned = await request.post(`${baseUrl}/api/devices/${d.id}/fsa-assignment`, {
      headers: AUTH,
      data: { fsaHref: `/api/fsas/${FSA_MRID}` },
    });
    expect(assigned.status()).toBe(200);
  }

  // Control: before the send, neither device's program list holds a control.
  const listHref = (id: string) => `/edev/${id}/fsa/${FSA_MRID}/derp/${templateId}/derc`;
  for (const d of devices) {
    const before = await readWire(PROTOCOL_PORT, getCertDir(), d.cert, listHref(d.id));
    expect(before.status).toBe(200);
    expect(before.body).toMatch(/<DERControlList [^>]*results="0"/);
    expect(before.body).not.toMatch(/<DERControl[\s>]/);
  }

  // Send the control from the Control tab, scheduled an hour ahead.
  await page.goto(`${baseUrl}/ui/control?token=${ADMIN_KEY}`);
  await page.getByTestId('control-target-fsa').check();
  await page.locator('#controlFsa').selectOption({ label: FSA_LABEL });
  await expect(page.getByTestId('fsa-control-reach')).toContainText('Reaches the 2 devices');
  await page.locator('#controlFsaProgram').selectOption({ label: TEMPLATE });
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
  const row = page.locator(`[data-testid="fsa-control-row"][data-mrid="${mrid}"]`);
  await expect(row.getByTestId('fsa-control-holding')).toHaveText('2 of 2 devices', { timeout: 5000 });
  await expect(row.getByTestId('fsa-control-status')).toHaveText('scheduled');

  // Each device reads the same control on its own wire list.
  for (const d of devices) {
    const wire = await readWire(PROTOCOL_PORT, getCertDir(), d.cert, listHref(d.id));
    expect(wire.status).toBe(200);
    expect(wire.body).toContain(`<mRID>${mrid}</mRID>`);
    expect(wire.body).toContain('<currentStatus>0</currentStatus>');
  }

  // Cancel from the UI, after the confirmation step.
  await page.getByTestId(`der-control-cancel-${mrid}`).click();
  await expect(row.getByTestId('fsa-control-status')).toHaveText('scheduled');
  await page.getByTestId(`der-control-confirm-cancel-${mrid}`).click();
  await expect(row.getByTestId('fsa-control-status')).toHaveText('cancelled', { timeout: 5000 });
  await expect(page.getByTestId(`der-control-cancel-${mrid}`)).toHaveCount(0);

  for (const d of devices) {
    const wire = await readWire(PROTOCOL_PORT, getCertDir(), d.cert, listHref(d.id));
    expect(wire.body).toContain(`<mRID>${mrid}</mRID>`);
    expect(wire.body).toContain('<currentStatus>2</currentStatus>');
  }
});
