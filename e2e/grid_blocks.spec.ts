import { test, expect } from '@playwright/test';
import { startServer, stopServer } from './setup';
import { AUTH, fillBlock, openDerms, seedFleet } from './fixtures/grid';

// #1159: every scheduled block on the DERMS tab, in a 24 hour timeline and a
// list. The batch is entered and confirmed through the page; both views read
// the server's list, and a cancel changes them only through the next read.

let baseUrl: string;

test.beforeEach(async ({ page }) => {
  baseUrl = await startServer();
  await page.setExtraHTTPHeaders(AUTH);
});

test.afterEach(() => {
  stopServer();
});

async function submitTwoBlocks(page: import('@playwright/test').Page) {
  await fillBlock(page, 0, { to: true, x: '50', minutes: '60' });
  await page.getByRole('button', { name: 'Add row' }).click();
  await fillBlock(page, 1, { to: false, x: '20', minutes: '30' });
  await page.getByRole('button', { name: 'Review' }).click();
  await page.getByRole('button', { name: 'Confirm' }).click();
  await expect(page.getByTestId('grid-created')).toContainText('with 2 blocks', { timeout: 10000 });
}

test('a submitted batch shows in the timeline and the list, and a cancelled block leaves both', async ({ page, request }) => {
  await seedFleet(request, baseUrl);
  await openDerms(page, baseUrl);
  await submitTwoBlocks(page);

  const timeline = page.getByTestId('grid-timeline');
  const list = page.getByTestId('grid-block');
  await expect(list).toHaveCount(2, { timeout: 10000 });
  await expect(timeline.getByTestId('events-bar')).toHaveCount(2);
  await expect(list.nth(0).getByTestId('grid-block-direction')).toHaveText('DISCHARGE');
  await expect(list.nth(1).getByTestId('grid-block-direction')).toHaveText('CHARGE');
  await expect(page.getByTestId('grid-timeline-discharge').getByTestId('events-bar')).toContainText('DISCHARGE 50.0 kW');
  await expect(page.getByTestId('grid-timeline-charge').getByTestId('events-bar')).toContainText('CHARGE 20.0 kW');
  await expect(page.getByTestId('grid-timeline-discharge').getByTestId('events-detail')).toBeVisible();
  await expect(page.getByTestId('grid-timeline-charge').getByTestId('events-detail')).toBeVisible();
  // The server has no delivery reading for a block nothing has run, and says so.
  await expect(list.nth(0).getByTestId('grid-block-delivered')).toHaveText('no reading');

  await page.getByRole('button', { name: /^Cancel block 2 of batch/ }).click();
  await page.getByRole('button', { name: /^Confirm cancel block 2 of batch/ }).click();

  await expect(list).toHaveCount(1, { timeout: 10000 });
  await expect(list.nth(0).getByTestId('grid-block-direction')).toHaveText('DISCHARGE');
  await expect(timeline.getByTestId('events-bar')).toHaveCount(1);
  await expect(page.getByTestId('grid-cancel-error')).toHaveCount(0);

  // The block is still on the server, cancelled; the filter only hides it.
  await page.getByTestId('grid-blocks-live-only').uncheck();
  await expect(list).toHaveCount(2);
  await expect(list.nth(1).getByTestId('grid-block-status')).toHaveText('cancelled');
});

// Screenshots for review, written only when a folder is named, so a normal
// run leaves no files behind.
const SHOTS = process.env.GRID_BLOCKS_SHOTS_DIR;

test('screenshots of the block views', async ({ page, request }) => {
  test.skip(!SHOTS, 'GRID_BLOCKS_SHOTS_DIR is not set');
  await seedFleet(request, baseUrl);
  await openDerms(page, baseUrl);
  await submitTwoBlocks(page);
  await expect(page.getByTestId('grid-block')).toHaveCount(2, { timeout: 10000 });
  const pane = page.getByTestId('grid-blocks-pane');
  await page.setViewportSize({ width: 1600, height: 1000 });
  await pane.screenshot({ path: `${SHOTS}/blocks-desktop.png` });
  await page.setViewportSize({ width: 400, height: 900 });
  await pane.screenshot({ path: `${SHOTS}/blocks-phone.png` });
});
