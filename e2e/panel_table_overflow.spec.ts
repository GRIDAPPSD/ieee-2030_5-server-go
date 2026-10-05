// Issue 884: a wide embedder table must stay inside its card and never paint
// over a neighbouring card. The panel replies are mocked at the browser, so
// the spec needs no embedder; layout is measured in a real browser because
// jsdom has no layout.
import { test, expect } from '@playwright/test';
import { startServer, stopServer } from './setup';

let baseUrl: string;

test.beforeAll(async () => {
  baseUrl = await startServer();
});

test.afterAll(() => {
  stopServer();
});

const columns = ['Time', 'Sender', 'Target', 'Payload', 'Bytes', 'Duration', 'Outcome'];
const cell = (text: string) => ({ kind: 'text', text });

const descriptor = {
  version: 2,
  sections: [
    {
      kind: 'definitionList',
      heading: 'Publishing',
      prose: [],
      empty: '',
      body: { groups: [{ heading: '', entries: [{ key: 'State', value: cell('on') }] }] },
    },
    {
      kind: 'table',
      heading: 'Recent sends',
      prose: [],
      empty: '',
      body: {
        columns,
        rows: [columns.map((c) => cell(`${c.toLowerCase()}-value-that-is-fairly-long-0123456789`))],
      },
    },
    {
      kind: 'definitionList',
      heading: 'Refusals',
      prose: [],
      empty: '',
      body: { groups: [{ heading: '', entries: [{ key: 'Count', value: cell('0') }] }] },
    },
  ],
};

test.beforeEach(async ({ page }) => {
  await page.setExtraHTTPHeaders({ Authorization: 'Bearer e2e-test-key' });
  await page.route('**/api/ui/panels', (route) =>
    route.fulfill({ json: [{ id: 'wide', label: 'Wide' }] }),
  );
  await page.route('**/api/ui/panels/wide', (route) => route.fulfill({ json: descriptor }));
});

test('a seven-column table stays inside its card and clear of the other cards', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(baseUrl + '/ui/wide?token=e2e-test-key');

  const cards = page.getByTestId('descriptor-section');
  await expect(cards).toHaveCount(3);
  await expect(page.getByTestId('descriptor-column-header')).toHaveCount(columns.length);

  const tableCard = cards.nth(1);
  const cardBox = await tableCard.boundingBox();
  if (!cardBox) throw new Error('no layout box for the table card');

  // What the table paints: its own box, unless it sits in a wrapper that
  // scrolls horizontally, in which case the wrapper's box is what shows.
  const painted = await tableCard.locator('table').evaluate((t) => {
    const w = t.parentElement as HTMLElement;
    const clips = w !== t.closest('.card') && getComputedStyle(w).overflowX === 'auto';
    const box = (clips ? w : t).getBoundingClientRect();
    return { right: box.right, naturalWidth: t.scrollWidth, room: w.clientWidth };
  });
  expect(painted.right).toBeLessThanOrEqual(cardBox.x + cardBox.width + 0.5);

  // No other card's box intersects the table card's box.
  for (const i of [0, 2]) {
    const other = await cards.nth(i).boundingBox();
    if (!other) throw new Error('no layout box for card ' + i);
    const overlapX = Math.min(cardBox.x + cardBox.width, other.x + other.width) - Math.max(cardBox.x, other.x);
    const overlapY = Math.min(cardBox.y + cardBox.height, other.y + other.height) - Math.max(cardBox.y, other.y);
    expect(overlapX > 0 && overlapY > 0).toBe(false);
  }
});
