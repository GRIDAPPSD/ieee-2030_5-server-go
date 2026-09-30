import { test, expect } from '@playwright/test';
import { join } from 'path';
import { startServer, stopServer } from './setup';

// Acceptance criterion 8: a cancel test, in its own spec file so the create
// flow in dashboard.spec.ts stays independent of it. Seeds the same fixture
// (e2e/fixtures/der-control.yaml): one device, one FSA, one DER program.
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
    'Authorization': 'Bearer e2e-test-key',
  });
});

test('cancelling a scheduled control requires confirmation, then the row shows cancelled', async ({ page }) => {
  await page.goto(baseUrl + '/ui/control?token=e2e-test-key');
  await page.waitForLoadState('domcontentloaded');

  await page.locator('#controlDevice').selectOption({ label: '222222222222' });
  await page.locator('#controlProgram').selectOption({ label: 'e2e der control program' });
  await page.locator('#controlType').selectOption('connect');
  await page.locator('#controlDuration').fill('60');

  await page.getByRole('button', { name: 'Send' }).click();
  await page.getByRole('button', { name: 'Confirm' }).click();

  const result = page.locator('#controlResult');
  await expect(result).toContainText(/[0-9A-F]{32}/, { timeout: 5000 });
  const mrid = (await result.textContent())?.match(/[0-9A-F]{32}/)?.[0];
  expect(mrid).toBeTruthy();

  const row = page.locator(`[data-testid="der-control-row"][data-mrid="${mrid}"]`);
  await expect(row).toBeVisible();
  // Started "now", so the derived status is active or scheduled depending
  // on second-boundary timing; either way it is not yet cancelled, and
  // both statuses carry the Cancel button (lib/dercontrol.ts canCancel).
  await expect(row.getByTestId('der-control-status')).not.toContainText('cancelled');

  const cancelButton = page.getByTestId(`der-control-cancel-${mrid}`);
  await cancelButton.click();

  // Clicking Cancel only opens the inline confirmation; the control is
  // still not cancelled until Confirm is clicked too.
  await expect(row.getByTestId('der-control-status')).not.toContainText('cancelled');

  await page.getByTestId(`der-control-confirm-cancel-${mrid}`).click();

  await expect(row.getByTestId('der-control-status')).toContainText('cancelled', { timeout: 5000 });
  await expect(page.getByTestId(`der-control-cancel-${mrid}`)).toHaveCount(0);
});
