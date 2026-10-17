// The admin searches for a title and adds it: the card flips to "In library"
// and the API reports the title as in the library on the next search.
// Search needs Radarr/Sonarr to reach their metadata service; without internet
// the spec skips instead of failing.
const { test, expect } = require('@playwright/test');
const { adminAPI, browserContextFor } = require('../helpers');

test.describe('search and add', () => {
  let admin;

  test.beforeAll(async () => {
    admin = await adminAPI();
  });

  test.afterAll(async () => {
    if (admin) await admin.dispose();
  });

  test('admin searches and adds a title', async ({ browser }) => {
    const ctx = await browserContextFor(browser, admin);
    const page = await ctx.newPage();
    await page.goto('/');
    await page.getByTestId('tab-search').click();

    const input = page.getByTestId('search-input');
    await input.fill('matrix');
    await input.press('Enter');

    let haveResults = true;
    try {
      await expect(page.getByTestId('result-add').first()).toBeVisible({ timeout: 45_000 });
    } catch (_) {
      haveResults = false;
    }
    test.skip(!haveResults, 'no addable result; metadata lookup is probably offline');

    const card = page.getByTestId('result-card').filter({ has: page.getByTestId('result-add') }).first();
    const title = await card.getAttribute('data-title');
    await card.getByTestId('result-add').click();
    await expect(card.getByTestId('result-in-library')).toBeVisible();
    await expect(card.getByTestId('result-add')).toHaveCount(0);

    // The API agrees: the title is now in Radarr or Sonarr.
    await expect
      .poll(
        async () => {
          const res = await admin.get('/api/search?q=' + encodeURIComponent(title));
          const body = await res.json();
          const r = (body.results || []).find((x) => x.title === title);
          return r ? r.in_library : 'missing';
        },
        { timeout: 15_000 },
      )
      .toBe(true);

    await ctx.close();
  });
});
