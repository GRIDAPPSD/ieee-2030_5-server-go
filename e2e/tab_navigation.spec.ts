// Issue 561 step B: criteria 4 (back and forward) and 5 (hard reload of
// each tab path). The four pre-existing specs still navigate only to "/"
// (root, now the overview tab); this file is where deep-link and history
// behavior across the other four tabs gets covered, per criterion 11's
// "new tab tests go in a new spec file".
import { test, expect } from '@playwright/test';
import { startServer, stopServer } from './setup';

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer();
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders({
    Authorization: 'Bearer e2e-test-key',
  });
});

// One heading unique to each tab, to prove a reload rendered that tab's
// own cards rather than falling back to overview (issue 561's per-tab
// card partition, criterion 1, landed in step A).
const TAB_HEADING: Record<string, string> = {
  overview: 'Connected Devices',
  devices: 'End Devices',
  fsas: 'Create FSA Template',
  control: 'Send DER Control',
  certificates: 'Certificate Management',
};

// 1. Back and forward across three tabs follows aria-current (criterion 4)
test('back and forward across tabs keeps the URL and aria-current in step', async ({ page }) => {
  await page.goto(baseUrl + '/ui/overview?token=e2e-test-key');
  await expect(page.getByTestId('tab-overview')).toHaveAttribute('aria-current', 'page');

  await page.getByTestId('tab-devices').click();
  await expect(page).toHaveURL(baseUrl + '/ui/devices');
  await expect(page.getByTestId('tab-devices')).toHaveAttribute('aria-current', 'page');

  await page.getByTestId('tab-fsas').click();
  await expect(page).toHaveURL(baseUrl + '/ui/fsas');
  await expect(page.getByTestId('tab-fsas')).toHaveAttribute('aria-current', 'page');

  await page.goBack();
  await expect(page).toHaveURL(baseUrl + '/ui/devices');
  await expect(page.getByTestId('tab-devices')).toHaveAttribute('aria-current', 'page');

  await page.goForward();
  await expect(page).toHaveURL(baseUrl + '/ui/fsas');
  await expect(page.getByTestId('tab-fsas')).toHaveAttribute('aria-current', 'page');
});

// 2. A hard reload of each of the five tab paths renders that tab (criterion 5)
for (const [slug, heading] of Object.entries(TAB_HEADING)) {
  test(`hard reload of /ui/${slug} renders the ${slug} tab`, async ({ page }) => {
    await page.goto(baseUrl + `/ui/${slug}?token=e2e-test-key`);

    await expect(page.getByTestId(`tab-${slug}`)).toHaveAttribute('aria-current', 'page');
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
  });
}
