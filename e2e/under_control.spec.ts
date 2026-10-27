import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer } from './setup';
import { ADMIN_KEY } from './adminKey';

let baseUrl: string;

// Its own server, so no earlier spec has left a control in force: the count
// starts at 0 and the one control this test sends is the only one.
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
  await page.setExtraHTTPHeaders({ Authorization: `Bearer ${ADMIN_KEY}` });
});

test('overview counts a device under a control only once a control is in force', async ({ page }) => {
  await page.goto(baseUrl + `/?token=${ADMIN_KEY}`);
  await page.waitForLoadState('domcontentloaded');

  await expect(page.locator('#derUnderControl')).toHaveText('0');
  await expect(page.locator('#derControlUnknown')).toHaveCount(0);

  await page.getByTestId('tab-control').click();
  await page.locator('#controlDevice').selectOption({ label: '222222222222' });
  await page.locator('#controlProgram').selectOption({ label: 'e2e der control program' });
  await page.getByTestId('control-fn-opModConnect').check();
  await page.locator('#controlDuration').fill('5');
  await page.getByRole('button', { name: 'Send' }).click();
  await page.getByRole('button', { name: 'Confirm' }).click();
  await expect(page.locator('#controlResult')).toContainText(/[0-9A-F]{32}/, { timeout: 5000 });

  await page.getByTestId('tab-overview').click();
  await expect(page.locator('#derUnderControl')).toHaveText('1', { timeout: 15000 });
  await expect(page.locator('#derControlUnknown')).toHaveCount(0);
});
