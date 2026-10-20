import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer } from './setup';
import { ADMIN_KEY } from './adminKey';

// The Programs drawer of a device (#925), against the boot fixture of
// e2e/fixtures/der-control.yaml: device e2e0, FSA "0", fixture program p1.
// The server runs with no data directory, so the persistence banner shows.
let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer({
    SEP2_BOOT_FIXTURE: join(__dirname, 'fixtures', 'der-control.yaml'),
    SEP2_PEN: '12345',
  });
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders({
    'Authorization': `Bearer ${ADMIN_KEY}`,
  });
});

test('a device Programs drawer lists, adds, edits and deletes device programs', async ({ page }) => {
  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await page.getByRole('button', { name: 'Programs of device e2e0' }).click();

  const drawer = page.getByTestId('device-programs-e2e0');
  await expect(drawer.getByTestId('programs-persist-banner')).toContainText('nothing here survives a restart');

  const fixtureRow = drawer.getByTestId('program-row-p1');
  await expect(fixtureRow.getByTestId('program-origin')).toHaveText('Fixture');
  await expect(fixtureRow.getByTestId('program-restart')).toHaveText('Reseeded on restart');
  await expect(fixtureRow.getByRole('button', { name: 'Edit e2e der control program' })).toBeVisible();

  await drawer.getByTestId('program-add-0').click();
  await drawer.getByLabel('Description').fill('added by e2e');
  await drawer.getByLabel('Primacy').fill('1');
  await drawer.getByLabel('Default control').selectOption('connect');
  await drawer.getByRole('button', { name: 'Add device program' }).click();

  const addedRow = drawer.getByRole('row').filter({ hasText: 'added by e2e' });
  await expect(addedRow).toBeVisible();
  await expect(addedRow.getByTestId('program-origin')).toHaveText('Device');

  await addedRow.getByRole('button', { name: 'Edit added by e2e' }).click();
  await drawer.getByLabel('Description').fill('renamed by e2e');
  await drawer.getByRole('button', { name: 'Save device program' }).click();
  const renamedRow = drawer.getByRole('row').filter({ hasText: 'renamed by e2e' });
  await expect(renamedRow).toBeVisible();
  await renamedRow.getByRole('button', { name: 'Delete renamed by e2e' }).click();
  await renamedRow.getByRole('button', { name: 'Confirm delete renamed by e2e' }).click();
  await expect(renamedRow).toHaveCount(0);
  await expect(fixtureRow).toBeVisible();
});

test('the Control tab note about no programs opens the device drawer', async ({ page }) => {
  await page.goto(baseUrl + `/ui/devices?token=${ADMIN_KEY}`);
  await page.getByRole('button', { name: 'Programs of device e2e0' }).click();
  const drawer = page.getByTestId('device-programs-e2e0');
  await drawer.getByRole('button', { name: 'Delete e2e der control program' }).click();
  await drawer.getByRole('button', { name: 'Confirm delete e2e der control program' }).click();
  await expect(drawer.getByTestId('program-row-p1')).toHaveCount(0);

  await page.getByTestId('tab-control').click();
  await page.locator('#controlDevice').selectOption({ label: '222222222222' });
  await page.getByTestId('der-control-devices-link').click();

  await expect(page.getByTestId('device-programs-e2e0')).toBeVisible();
  await expect(page).toHaveURL(/\/ui\/devices$/);
});
