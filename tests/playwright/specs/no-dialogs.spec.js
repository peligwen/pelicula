// tests/playwright/specs/no-dialogs.spec.js
//
// Guards against regressions to browser alert()/prompt() dialogs in the
// search, users, and settings flows. Each test registers a dialog handler
// that throws immediately — any unexpected dialog will fail the test.
//
// All backend endpoints are stubbed via page.route() so no live stack is
// required. Auth is simulated by stubbing GET /api/pelicula/auth/check.

'use strict';

const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

// ── Shared auth stubs ───────────────────────────────────────────────────────

/** Stub auth/check to return a viewer (non-admin) session. */
async function stubViewerAuth(page) {
    await page.route('**/api/pelicula/auth/check', route =>
        route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ valid: true, role: 'viewer', username: 'testviewer' }),
        })
    );
}

/** Stub auth/check to return an admin session. */
async function stubAdminAuth(page) {
    await page.route('**/api/pelicula/auth/check', route =>
        route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ valid: true, role: 'admin', username: 'admin' }),
        })
    );
}

/** Register a dialog guard that throws on any unexpected browser dialog. */
function guardDialogs(page) {
    page.on('dialog', d => {
        throw new Error('Unexpected browser dialog: ' + d.message());
    });
}

/** Stub the search endpoint to return one movie result. */
async function stubSearch(page) {
    await page.route('**/api/pelicula/search**', route =>
        route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
                results: [{
                    type: 'movie',
                    title: 'Test Film',
                    year: 2024,
                    tmdbId: 99999,
                    tvdbId: 0,
                    poster: '',
                    added: false,
                    overview: 'A test film.',
                    rating: 7.5,
                    certification: 'PG',
                    runtime: 120,
                    network: null,
                    seasonCount: 0,
                }],
            }),
        })
    );
}

/** Stub downstream calls that fire on page load to prevent noise. */
async function stubCommonPageLoad(page) {
    // Suppress noisy endpoints that aren't under test.
    await page.route('**/api/pelicula/status**',       route => route.fulfill({ status: 200, contentType: 'application/json', body: '{}' }));
    await page.route('**/api/pelicula/network**',      route => route.fulfill({ status: 200, contentType: 'application/json', body: '{"containers":[]}' }));
    await page.route('**/api/pelicula/sessions**',     route => route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }));
    await page.route('**/api/pelicula/catalog**',      route => route.fulfill({ status: 200, contentType: 'application/json', body: '{"movies":[],"shows":[]}' }));
    await page.route('**/api/pelicula/jobs**',         route => route.fulfill({ status: 200, contentType: 'application/json', body: '{"groups":{}}' }));
}

// ── Test 1: search request → 403 → toast, not alert ─────────────────────────

test('search request 403 → toast error, no dialog', async ({ page }) => {
    guardDialogs(page);
    await stubViewerAuth(page);
    await stubSearch(page);
    await stubCommonPageLoad(page);

    // Stub requests POST to return null body with 403
    await page.route('**/api/pelicula/requests', route => {
        if (route.request().method() === 'POST') {
            return route.fulfill({ status: 403, contentType: 'application/json', body: 'null' });
        }
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    });

    await page.goto('/');
    // Wait for auth to resolve (login overlay should stay hidden — stubbed as valid).
    await expect(page.locator('[data-testid="login-overlay"]')).toBeHidden({ timeout: 10_000 });

    // Type in the search box to get results.
    await page.fill('[data-testid="search-input"]', 'Test Film');

    // Wait for the Request button to appear.
    const requestBtn = page.locator('[data-testid="search-request-btn"]').first();
    await expect(requestBtn).toBeVisible({ timeout: 5_000 });
    await requestBtn.click();

    // The toast should appear with an error class.
    const toast = page.locator('#notify-toast');
    await expect(toast).toHaveClass(/toast-error/, { timeout: 5_000 });
    await expect(toast).toContainText('not authorized');
});

// ── Test 2: search request → network error → toast, not alert ───────────────

test('search request network error → toast error, no dialog', async ({ page }) => {
    guardDialogs(page);
    await stubViewerAuth(page);
    await stubSearch(page);
    await stubCommonPageLoad(page);

    // Abort the request POST to simulate a network error.
    await page.route('**/api/pelicula/requests', route => {
        if (route.request().method() === 'POST') {
            return route.abort();
        }
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    });

    await page.goto('/');
    await expect(page.locator('[data-testid="login-overlay"]')).toBeHidden({ timeout: 10_000 });

    await page.fill('[data-testid="search-input"]', 'Test Film');
    const requestBtn = page.locator('[data-testid="search-request-btn"]').first();
    await expect(requestBtn).toBeVisible({ timeout: 5_000 });
    await requestBtn.click();

    const toast = page.locator('#notify-toast');
    await expect(toast).toHaveClass(/toast-error/, { timeout: 5_000 });
    await expect(toast).toHaveClass(/visible/);
});

// ── Test 3: deny-request inline composer appears, submits with reason ────────

test('deny-request inline composer: appears, submits with reason, no dialog', async ({ page }) => {
    guardDialogs(page);
    await stubAdminAuth(page);
    await stubCommonPageLoad(page);

    const MOCK_REQUEST = [{
        id: '42',
        type: 'movie',
        title: 'Denied Film',
        year: 2024,
        poster: '',
        state: 'pending',
        requested_by: 'someviewer',
        reason: null,
    }];

    // Stub GET requests to return one pending request.
    await page.route('**/api/pelicula/requests', route => {
        if (route.request().method() === 'GET') {
            return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(MOCK_REQUEST) });
        }
        return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    // Capture the deny POST body.
    let deniedWith = null;
    await page.route('**/api/pelicula/requests/42/deny', route => {
        const body = route.request().postDataJSON();
        deniedWith = body;
        return route.fulfill({ status: 200, contentType: 'application/json', body: '{"id":"42","state":"denied"}' });
    });

    // After deny, return empty list so loadRequests() clears the list.
    // (Already handled above — second GET will still return MOCK_REQUEST but that's fine for this test.)

    await page.goto('/');
    await expect(page.locator('[data-testid="login-overlay"]')).toBeHidden({ timeout: 10_000 });

    // The deny button is inside the request item rendered into #requests-pending-list.
    const denyBtn = page.locator('.request-item[data-id="42"] [data-action="start-deny-request"]');
    await expect(denyBtn).toBeVisible({ timeout: 8_000 });
    await denyBtn.click();

    // The inline composer should now be visible.
    const form = page.locator('.request-item[data-id="42"] .request-deny-form');
    await expect(form).not.toHaveClass(/hidden/, { timeout: 3_000 });

    // The .request-actions cluster should be hidden.
    const actions = page.locator('.request-item[data-id="42"] .request-actions');
    await expect(actions).toHaveClass(/hidden/, { timeout: 1_000 });

    // Type a reason and submit.
    const textarea = form.locator('.request-deny-input');
    await textarea.fill('Wrong quality');
    await form.locator('button[type="submit"]').click();

    // Assert the API was called with the reason.
    await expect.poll(() => deniedWith, { timeout: 5_000 }).toMatchObject({ reason: 'Wrong quality' });
});

// ── Test 4: deny-request → 500 → inline .users-error, no dialog ─────────────

test('deny-request 500 → .users-error shown, no dialog', async ({ page }) => {
    guardDialogs(page);
    await stubAdminAuth(page);
    await stubCommonPageLoad(page);

    const MOCK_REQUEST = [{
        id: '43',
        type: 'movie',
        title: 'Error Film',
        year: 2024,
        poster: '',
        state: 'pending',
        requested_by: 'someviewer',
        reason: null,
    }];

    await page.route('**/api/pelicula/requests', route => {
        if (route.request().method() === 'GET') {
            return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(MOCK_REQUEST) });
        }
        return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    // Stub deny endpoint to 500.
    await page.route('**/api/pelicula/requests/43/deny', route =>
        route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'server blew up' }) })
    );

    await page.goto('/');
    await expect(page.locator('[data-testid="login-overlay"]')).toBeHidden({ timeout: 10_000 });

    const denyBtn = page.locator('.request-item[data-id="43"] [data-action="start-deny-request"]');
    await expect(denyBtn).toBeVisible({ timeout: 8_000 });
    await denyBtn.click();

    // Submit without a reason.
    const form = page.locator('.request-item[data-id="43"] .request-deny-form');
    await expect(form).not.toHaveClass(/hidden/, { timeout: 3_000 });
    await form.locator('button[type="submit"]').click();

    // The per-row .users-error slot should show the error.
    const errSlot = page.locator('.request-item[data-id="43"] .users-error');
    await expect(errSlot).not.toHaveClass(/hidden/, { timeout: 5_000 });
    await expect(errSlot).toContainText('Deny failed');
});

// ── Test 5: settings unblock → 500 → inline error, no dialog ────────────────

test('settings unblock 500 → inline .unblock-error shown, no dialog', async ({ page }) => {
    guardDialogs(page);
    await stubAdminAuth(page);
    await stubCommonPageLoad(page);

    // Stub settings read.
    await page.route('**/api/pelicula/settings**', route =>
        route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
    );

    // Stub arr-meta.
    await page.route('**/api/pelicula/arr-meta**', route =>
        route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
    );

    // Stub GET blocked-releases to return one row.
    await page.route('**/api/procula/blocked-releases', route => {
        if (route.request().method() === 'GET') {
            return route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify([{
                    id: 77,
                    display_title: 'Blocked Movie (2024)',
                    file_path: '/media/blocked-movie-2024.mkv',
                    arr_app: 'radarr',
                    reason: 'corrupt',
                    blocked_at: '2024-01-15T00:00:00Z',
                }]),
            });
        }
        // DELETE → fail
        return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'cannot unblock' }) });
    });

    await page.goto('/');
    await expect(page.locator('[data-testid="login-overlay"]')).toBeHidden({ timeout: 10_000 });

    // Navigate to settings tab.
    await page.click('[data-tab="settings"]');
    await page.waitForFunction(() => document.body.dataset.tab === 'settings', { timeout: 5_000 });

    // Wait for the blocked release row to render.
    const unblockBtn = page.locator('#st-blocked-releases-list button').filter({ hasText: 'Unblock' }).first();
    await expect(unblockBtn).toBeVisible({ timeout: 8_000 });
    await unblockBtn.click();

    // An inline .unblock-error span should appear.
    const errSpan = page.locator('.unblock-error').first();
    await expect(errSpan).toBeVisible({ timeout: 5_000 });
    await expect(errSpan).toContainText('Unblock failed');
});
