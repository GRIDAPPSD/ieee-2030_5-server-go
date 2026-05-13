// IEEE-096: FSA hierarchy management dashboard tests.
//
// Each test creates its own admin FSA (UI or API) and cleans up after
// itself via the API. No reliance on prior fixture state.

import { test, expect, Page } from '@playwright/test';
import { startServer, stopServer } from './setup';

let baseUrl: string;
const adminKey = 'e2e-test-key';
const authHeaders = { Authorization: 'Bearer ' + adminKey };

test.beforeAll(async () => {
  baseUrl = await startServer();
});

test.afterAll(() => {
  stopServer();
});

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders(authHeaders);
});

// page.request does NOT inherit setExtraHTTPHeaders, so wrap calls.
const apiGet = (page: Page, url: string) =>
  page.request.get(url, { headers: authHeaders });
const apiPost = (page: Page, url: string, data: any) =>
  page.request.post(url, { headers: authHeaders, data });
const apiDelete = (page: Page, url: string) =>
  page.request.delete(url, { headers: authHeaders });

// 1) Panels render.
test('IEEE-096: FSA Tree panel and Create FSA form render', async ({ page }) => {
  await page.goto(baseUrl + '/?token=' + adminKey);
  await page.waitForLoadState('domcontentloaded');

  await expect(page.getByText('Create FSA Template')).toBeVisible();
  await expect(page.getByText(/FSA Tree/)).toBeVisible();
  await expect(page.locator('#newFSADesc')).toBeVisible();
  await expect(page.locator('#topologyTree')).toBeAttached();
});

// 2) Topology API smoke: SY -> FD -> SP root.
test('IEEE-096: GET /api/topology returns SY -> FD -> SP root', async ({ page }) => {
  await page.goto(baseUrl + '/?token=' + adminKey);
  const r = await apiGet(page, baseUrl + '/api/topology');
  expect(r.status()).toBe(200);
  const tree = await r.json();
  expect(tree.kind).toBe('SY');
  expect(tree.children).toBeDefined();
  expect(tree.children.length).toBe(1);
  expect(tree.children[0].kind).toBe('FD');
  expect(tree.children[0].children.length).toBe(1);
  expect(tree.children[0].children[0].kind).toBe('SP');
});

// 3) Full UI: create FSA via the form, confirm it appears in the tree.
test('IEEE-096: creating an FSA via the dashboard populates the tree', async ({ page }) => {
  const mRID = 'e2e-fsa-' + Date.now();

  await page.goto(baseUrl + '/?token=' + adminKey);
  await page.waitForLoadState('domcontentloaded');

  await page.locator('#newFSADesc').fill('E2E created FSA');
  await page.locator('#newFSAMRID').fill(mRID);
  await page.locator('#newFSAPrimacy').fill('1');
  await page.getByRole('button', { name: 'Create FSA' }).click();

  await expect(page.locator('#createFSAResult')).toContainText(mRID, { timeout: 5000 });
  await expect(page.locator('#topologyTree')).toContainText(mRID, { timeout: 5000 });
  await expect(page.locator('#topologyTree')).toContainText('E2E created FSA');

  // Verify via API.
  const r = await apiGet(page, baseUrl + '/api/fsas/' + encodeURIComponent(mRID));
  expect(r.status()).toBe(200);
  const body = await r.json();
  expect(body.mRID).toBe(mRID);
  expect(body.description).toBe('E2E created FSA');

  // Cleanup.
  const d = await apiDelete(page, baseUrl + '/api/fsas/' + encodeURIComponent(mRID));
  expect(d.status()).toBe(204);
});

// 4) Per-FSA Attach + Delete controls render in the tree.
test('IEEE-096: per-FSA controls render in the tree', async ({ page }) => {
  const mRID = 'e2e-ctrl-' + Date.now();

  await page.goto(baseUrl + '/?token=' + adminKey);
  await page.waitForLoadState('domcontentloaded');

  const created = await apiPost(page, baseUrl + '/api/fsas', {
    description: 'controls-test',
    mRID,
  });
  expect(created.status()).toBe(201);

  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect(page.locator('#topologyTree')).toContainText(mRID, { timeout: 5000 });
  await expect(page.locator('#attachInp-' + mRID)).toBeAttached();
  await expect(page.locator('button:has-text("Delete")').first()).toBeVisible();

  await apiDelete(page, baseUrl + '/api/fsas/' + encodeURIComponent(mRID));
});

// 5) Full lifecycle: API create + topology shows FSA as unassigned
//    template at the SY root.
test('IEEE-096: created FSA appears as unassigned template in topology', async ({ page }) => {
  const mRID = 'e2e-life-' + Date.now();

  await page.goto(baseUrl + '/?token=' + adminKey);
  await page.waitForLoadState('domcontentloaded');

  const c = await apiPost(page, baseUrl + '/api/fsas', {
    description: 'Lifecycle FSA',
    mRID,
  });
  expect(c.status()).toBe(201);

  const r = await apiGet(page, baseUrl + '/api/topology');
  const tree = await r.json();
  const unassigned = (tree.fsas || []) as Array<{ mRID: string }>;
  const found = unassigned.some((f) => f.mRID === mRID);
  expect(found).toBe(true);

  await apiDelete(page, baseUrl + '/api/fsas/' + encodeURIComponent(mRID));
});
