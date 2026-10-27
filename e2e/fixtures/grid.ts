import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { join } from 'path';
import { getCertDir } from '../setup';
import { ADMIN_KEY } from '../adminKey';
import { mintDeviceCert } from './device_cert';

// Shared by the grid block specs: an aggregator with two managed devices
// seeded through the admin API the way the Devices tab stores them, the DERMS
// tab opened, and a block typed into the entry form.

export const AUTH = { Authorization: `Bearer ${ADMIN_KEY}` };
const SERVER = join(__dirname, '..', '..', 'sep2server');

async function register(request: APIRequestContext, baseUrl: string, name: string, kind: string): Promise<string> {
  const cert = mintDeviceCert(SERVER, getCertDir(), name);
  const info = await request.post(`${baseUrl}/api/certs/info`, {
    headers: { ...AUTH, 'Content-Type': 'application/x-pem-file' },
    data: cert.certPEM,
  });
  expect(info.status()).toBe(200);
  const { sfdi, lfdi } = await info.json();
  const added = await request.post(`${baseUrl}/api/devices`, {
    headers: AUTH,
    data: { sfdi, lfdi, name, kind, pin: 424242, enabled: true },
  });
  expect(added.status()).toBe(201);
  return String(lfdi).toUpperCase();
}

// seedFleet registers an aggregator with two managed devices.
export async function seedFleet(request: APIRequestContext, baseUrl: string) {
  const aggregator = await register(request, baseUrl, 'grid-agg', 'aggregator');
  for (const name of ['grid-dev-a', 'grid-dev-b']) {
    const managed = await register(request, baseUrl, name, 'device');
    const pair = await request.post(`${baseUrl}/api/management-pairs`, {
      headers: AUTH,
      data: { managerLFDI: aggregator, managedLFDI: managed },
    });
    expect(pair.status()).toBeLessThan(300);
  }
}

export async function openDerms(page: Page, baseUrl: string) {
  await page.goto(`${baseUrl}/ui/derms?token=${ADMIN_KEY}`);
  await expect(page.getByTestId('grid-request-pane')).toBeVisible({ timeout: 10000 });
}

export async function fillBlock(page: Page, n: number, v: { to: boolean; x: string; minutes: string }) {
  const row = page.getByTestId('grid-row').nth(n);
  await row.getByRole('radio', { name: v.to ? /^To grid/ : /^From grid/ }).check();
  await row.getByTestId('grid-x').fill(v.x);
  await row.getByTestId('grid-duration').fill(v.minutes);
}
