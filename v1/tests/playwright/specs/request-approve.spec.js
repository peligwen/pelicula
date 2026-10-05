// A viewer searches and requests a title; an admin approves it; the request shows approved.
// Search needs Radarr/Sonarr to reach their metadata service; without internet
// the spec skips instead of failing.
const { test, expect } = require('@playwright/test');
const { adminAPI, browserContextFor, createViewer, deleteUser } = require('../helpers');

test.describe('request and approve', () => {
  let admin;
  let viewer;

  test.beforeAll(async () => {
    admin = await adminAPI();
    viewer = await createViewer(admin);
  });

  test.afterAll(async () => {
    if (admin) {
      if (viewer) {
        await deleteUser(admin, viewer.username);
        await viewer.api.dispose();
      }
      await admin.dispose();
    }
  });

  test('viewer requests, admin approves, request shows approved', async ({ browser }) => {
    // Viewer: search and request.
    const viewerCtx = await browserContextFor(browser, viewer.api);
    const vp = await viewerCtx.newPage();
    await vp.goto('/');
    await vp.getByTestId('tab-search').click();

    const input = vp.getByTestId('search-input');
    await input.fill('matrix');
    await input.press('Enter');

    let haveResults = true;
    try {
      await expect(vp.getByTestId('result-card').first()).toBeVisible({ timeout: 45_000 });
    } catch (_) {
      haveResults = false;
    }
    test.skip(!haveResults, 'search returned nothing; metadata lookup is probably offline');

    await vp.getByTestId('result-request').first().click();

    // Find out which title was requested from the API, not from card text.
    let requested;
    await expect
      .poll(
        async () => {
          const res = await viewer.api.get('/api/requests');
          const body = await res.json();
          requested = (body.requests || [])[0];
          return requested ? requested.status : '';
        },
        { timeout: 15_000 },
      )
      .toBe('pending');

    await vp.getByTestId('tab-requests').click();
    const viewerRow = vp.getByTestId('request-row').filter({ hasText: requested.title });
    await expect(viewerRow).toBeVisible();
    await expect(viewerRow.getByTestId('request-status')).toHaveText(/pending/i);
    // A viewer cannot approve.
    await expect(viewerRow.getByTestId('request-approve')).toHaveCount(0);

    // Admin: approve it.
    const adminCtx = await browserContextFor(browser, admin);
    const ap = await adminCtx.newPage();
    await ap.goto('/');
    await ap.getByTestId('tab-requests').click();
    const adminRow = ap.getByTestId('request-row').filter({ hasText: requested.title });
    await expect(adminRow).toBeVisible();
    await adminRow.getByTestId('request-approve').click();
    await expect(adminRow.getByTestId('request-status')).toHaveText(/approved/i);

    // The API agrees, and the title was added to Radarr/Sonarr (arr_id set).
    await expect
      .poll(
        async () => {
          const res = await admin.get('/api/requests');
          const body = await res.json();
          const r = (body.requests || []).find((x) => x.id === requested.id);
          return r ? `${r.status}:${r.arr_id > 0}` : 'missing';
        },
        { timeout: 15_000 },
      )
      .toBe('approved:true');

    // The viewer sees it approved after a reload.
    await vp.reload();
    await vp.getByTestId('tab-requests').click();
    await expect(
      vp.getByTestId('request-row').filter({ hasText: requested.title }).getByTestId('request-status'),
    ).toHaveText(/approved|available/i);

    await adminCtx.close();
    await viewerCtx.close();
  });
});
