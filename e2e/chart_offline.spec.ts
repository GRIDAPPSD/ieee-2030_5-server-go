// #364: the activity chart's library is bundled into the embedded admin UI
// rather than fetched at runtime from a public CDN. This spec asserts the property
// that bundling was for: the chart DRAWS on a deployment with no route to
// the public internet.
//
// Egress is blocked by aborting every request whose host is not the local
// admin listener, and the block is proved to work inside the same test
// (see the control below) so a passing run cannot mean "the interceptor
// never fired". The second describe is the discriminating control for the
// canvas assertion: the pre-Svelte page, which loads no chart library,
// produces no canvas at all.

import { test, expect, Page } from '@playwright/test';
import { startServer, stopServer } from './setup';

const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]', '::1']);

// blockExternalOrigins aborts every non-local request and returns the
// array recording what was attempted, so the test can assert both "the
// page asked for nothing external" and "an external request would have
// been caught".
async function blockExternalOrigins(page: Page): Promise<string[]> {
  const attempted: string[] = [];
  await page.route('**/*', async (route) => {
    const url = new URL(route.request().url());
    if (LOCAL_HOSTS.has(url.hostname)) {
      await route.continue();
      return;
    }
    attempted.push(route.request().url());
    await route.abort('blockedbyclient');
  });
  return attempted;
}

test.describe.configure({ mode: 'serial' });

test.describe('admin UI with the chart library bundled', () => {
  let baseUrl: string;

  test.beforeAll(async () => {
    baseUrl = await startServer();
  });

  test.afterAll(() => {
    stopServer();
  });

  test.beforeEach(async ({ page }) => {
    await page.setExtraHTTPHeaders({ Authorization: 'Bearer e2e-test-key' });
  });

  test('the activity chart draws with every external origin blocked', async ({ page }) => {
    const attempted = await blockExternalOrigins(page);

    await page.goto(baseUrl + '/?token=e2e-test-key');
    await page.waitForLoadState('domcontentloaded');

    const canvas = page.locator('#activityChart canvas').first();
    await expect(canvas).toBeAttached({ timeout: 10000 });

    const size = await canvas.evaluate((el) => {
      const c = el as HTMLCanvasElement;
      return { width: c.width, height: c.height };
    });
    expect(size.width).toBeGreaterThan(0);
    expect(size.height).toBeGreaterThan(0);

    // The chart initialized: an ECharts instance paints into a canvas and
    // reports no init error on the page.
    await expect(page.locator('[data-testid="chart-error"]')).toHaveCount(0);

    // A canvas of the right size could still be blank, so read the pixels
    // back: "drew" means something was actually painted.
    const paintedPixels = await canvas.evaluate((el) => {
      const c = el as HTMLCanvasElement;
      const ctx = c.getContext('2d');
      if (ctx === null) return -1;
      const { data } = ctx.getImageData(0, 0, c.width, c.height);
      let painted = 0;
      for (let i = 3; i < data.length; i += 4) {
        if (data[i] !== 0) painted++;
      }
      return painted;
    });
    expect(paintedPixels).toBeGreaterThan(0);

    // Control for the pixel count: the same measurement over a blank
    // canvas of the same size returns 0, so a positive count above is a
    // real reading and not an artifact of how it is taken.
    const blankPixels = await page.evaluate((size) => {
      const c = document.createElement('canvas');
      c.width = size.width;
      c.height = size.height;
      const ctx = c.getContext('2d');
      if (ctx === null) return -1;
      const { data } = ctx.getImageData(0, 0, c.width, c.height);
      let painted = 0;
      for (let i = 3; i < data.length; i += 4) {
        if (data[i] !== 0) painted++;
      }
      return painted;
    }, size);
    expect(blankPixels).toBe(0);

    // Item 3: the page fetched nothing from any external origin.
    expect(attempted).toEqual([]);

    // Control: prove the block and the recorder can both fire. Without
    // this, an interceptor that never ran would produce the same empty
    // array as a page that genuinely made no external request.
    // The probe host is a reserved-invalid name, deliberately not a real
    // CDN URL: this repo is asserted elsewhere to contain zero of those,
    // and a live URL written here just to be blocked would spoil that
    // count for every future scan.
    const probeUrl = 'https://blocked-egress-probe.invalid/echarts.min.js';
    const probeResult = await page.evaluate(
      (url) =>
        fetch(url)
          .then(() => 'ALLOWED')
          .catch(() => 'BLOCKED'),
      probeUrl,
    );
    expect(probeResult).toBe('BLOCKED');
    expect(attempted).toContain(probeUrl);
  });
});

test.describe('legacy dashboard behind the rollback flag', () => {
  let baseUrl: string;

  test.beforeAll(async () => {
    baseUrl = await startServer({ SEP2_ADMIN_LEGACY_DASHBOARD: 'true' });
  });

  test.afterAll(() => {
    stopServer();
  });

  test.beforeEach(async ({ page }) => {
    await page.setExtraHTTPHeaders({ Authorization: 'Bearer e2e-test-key' });
  });

  test('serves the pre-Svelte page, which draws no chart and fetches no CDN', async ({ page }) => {
    const attempted = await blockExternalOrigins(page);

    await page.goto(baseUrl + '/?token=e2e-test-key');
    await page.waitForLoadState('domcontentloaded');

    // The rollback really is the old page.
    await expect(page.locator('#hwSerial')).toBeVisible();
    await expect(page.locator('#chartNote')).toContainText('/ui/');

    // The discriminating control for the assertion above: this page has no
    // chart library, so no canvas is created. A canvas assertion that
    // passed here would prove nothing about the bundle.
    await expect(page.locator('#activityChart canvas')).toHaveCount(0);

    // The CDN script tag is gone from this page too, so even the rollback
    // path makes no external request.
    expect(attempted).toEqual([]);
  });
});
