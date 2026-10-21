import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer, getCertDir } from './setup';
import { ADMIN_KEY } from './adminKey';
import { mintDeviceCert } from './fixtures/device_cert';
import { readWire } from './fixtures/fsa_wire';

// #1053: a device is taken off an FSA from its Programs drawer. The device
// needs a certificate the server's CA signed to read its own wire list, so
// it, the FSA, its program and the assignment are seeded through the admin
// API; the unassign itself goes only through the UI, and the effect is read
// as the device, over mutual TLS on the protocol port.

const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const FSA_MRID = 'e2e-fsa-unassign';
const FSA_LABEL = 'unassign FSA';
const TEMPLATE = 'unassign program';
const SERVER = join(__dirname, '..', 'sep2server');
// Outside the 18443-20442 range setup.ts picks its own ports from, and
// apart from fsa_control.spec.ts's 21000 range.
const PROTOCOL_PORT = 22000 + Math.floor(Math.random() * 1000);

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

test('Unassign from FSA in the Programs drawer takes the FSA programs off the device wire list', async ({ page, request }) => {
  await expect
    .poll(async () => (await request.get(`${baseUrl}/api/fsas`, { headers: AUTH }).catch(() => null))?.status(), { timeout: 15000 })
    .toBe(200);

  const cert = mintDeviceCert(SERVER, getCertDir(), 'fsa-unassign');
  const info = await request.post(`${baseUrl}/api/certs/info`, {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: cert.certPEM,
  });
  expect(info.status()).toBe(200);
  const { sfdi, lfdi } = await info.json();
  const added = await request.post(`${baseUrl}/api/devices`, {
    headers: AUTH,
    data: { sfdi, lfdi, name: 'fsa-unassign', pin: 424242, enabled: true },
  });
  expect(added.status()).toBe(201);
  const id = String((await added.json()).href).split('/').pop()!;

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
  const assigned = await request.post(`${baseUrl}/api/devices/${id}/fsa-assignment`, {
    headers: AUTH,
    data: { fsaHref: `/api/fsas/${FSA_MRID}` },
  });
  expect(assigned.status()).toBe(200);

  // Control: while assigned, the device's wire list holds the program.
  const listHref = `/edev/${id}/fsa/${FSA_MRID}/derp`;
  const before = await readWire(PROTOCOL_PORT, getCertDir(), cert, listHref);
  expect(before.status).toBe(200);
  expect(before.body).toMatch(/<DERProgramList [^>]*results="1"/);

  await page.goto(`${baseUrl}/ui/devices?token=${ADMIN_KEY}`);
  await page.getByRole('button', { name: `Programs of device ${id}` }).click();
  const drawer = page.getByTestId(`device-programs-${id}`);
  const button = drawer.getByRole('button', { name: `Unassign from FSA ${FSA_LABEL} (${FSA_MRID})` });
  await button.click();
  await drawer.getByRole('button', { name: `Confirm unassign from ${FSA_LABEL} (${FSA_MRID})` }).click();

  // The group is gone from the drawer once the device is off the FSA.
  await expect(drawer.getByTestId(`programs-fsa-${FSA_MRID}`)).toHaveCount(0);

  const after = await readWire(PROTOCOL_PORT, getCertDir(), cert, listHref);
  expect(after.body).not.toMatch(/<DERProgramList [^>]*results="1"/);
  expect(after.body).not.toContain(`/derp/`);
  const gone = await request.get(`${baseUrl}/api/fsas`, { headers: AUTH });
  const fsas = (await gone.json()).fsas as { mRID: string; devices?: string[] }[];
  expect(fsas.find((f) => f.mRID === FSA_MRID)?.devices ?? []).not.toContain(id);
});
