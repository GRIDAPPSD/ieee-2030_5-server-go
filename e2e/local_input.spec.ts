import { test, expect } from '@playwright/test';
import { localInput } from './fixtures/local_input';

// Chromium normalizes a datetime-local value with zero seconds to HH:MM, and
// fill() throws "Malformed value" when the read-back differs from what it wrote.

test('localInput formats a :00 date without seconds and a :07 date with them', () => {
  expect(localInput(new Date(2026, 9, 21, 19, 14, 0))).toBe('2026-10-21T19:14');
  expect(localInput(new Date(2026, 9, 21, 19, 14, 7))).toBe('2026-10-21T19:14:07');
});

for (const seconds of [0, 1, 7, 59]) {
  test(`a step=1 datetime-local keeps the localInput value for :${seconds}`, async ({ page }) => {
    await page.setContent('<input id="at" type="datetime-local" step="1">');
    const value = localInput(new Date(2026, 9, 21, 19, 14, seconds));
    await page.locator('#at').fill(value);
    await expect(page.locator('#at')).toHaveValue(value);
  });
}
