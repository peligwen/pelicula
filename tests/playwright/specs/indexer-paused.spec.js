// tests/playwright/specs/indexer-paused.spec.js
// Paused-indexer surfacing: when /api/pelicula/status reports indexers that
// Prowlarr has disabled after repeated failures (indexers_paused), the
// dashboard shows a banner in the main pane and a count badge on the
// Prowlarr sidebar row. Both must clear when the list is empty.
const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

const DESKTOP_VIEWPORT = { width: 1280, height: 900 };

const ALL_SERVICES = ['prowlarr', 'sonarr', 'radarr', 'qbittorrent', 'procula', 'jellyfin', 'bazarr'];

// Mock /api/pelicula/status with a configurable indexers_paused list, plus
// /api/pelicula/health so VPN state can't leak into the banner area.
async function mockStatus(page, { paused = [] } = {}) {
    await page.route('**/api/pelicula/status', async (route) => {
        const services = {};
        for (const name of ALL_SERVICES) services[name] = 'up';
        await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
                status: 'ok',
                services,
                indexers: 4,
                indexers_paused: paused,
            }),
        });
    });
    await page.route('**/api/pelicula/health', async (route) => {
        await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ vpn: { port_status: 'ok' } }),
        });
    });
}

function inOneHour() {
    return new Date(Date.now() + 3600_000).toISOString();
}

test.describe('Paused-indexer surfacing', () => {
    test('banner and badge appear for a single paused indexer', async ({ page }) => {
        await mockStatus(page, {
            paused: [{ id: 5, name: 'Nyaa.si', disabledTill: inOneHour() }],
        });
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);

        const banner = page.locator('#indexer-paused-status');
        await expect(banner).toHaveClass(/visible/);
        await expect(page.locator('#indexer-paused-msg')).toContainText('Nyaa.si');
        await expect(page.locator('#indexer-paused-msg')).toContainText('until');

        const badge = page.locator('#svc-badge-prowlarr');
        await expect(badge).toBeVisible();
        await expect(badge).toHaveText('1 paused');
    });

    test('badge counts multiple paused indexers and message names each', async ({ page }) => {
        await mockStatus(page, {
            paused: [
                { id: 2, name: 'Alpha', disabledTill: inOneHour() },
                { id: 5, name: 'Beta', disabledTill: inOneHour() },
            ],
        });
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);

        await expect(page.locator('#indexer-paused-msg')).toContainText('Alpha');
        await expect(page.locator('#indexer-paused-msg')).toContainText('Beta');
        await expect(page.locator('#svc-badge-prowlarr')).toHaveText('2 paused');
    });

    test('banner and badge stay hidden when nothing is paused', async ({ page }) => {
        await mockStatus(page, { paused: [] });
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);

        // Prove checkServices ran before asserting the negatives.
        await expect(page.locator('#svc-pip-prowlarr')).toHaveClass(/up/);
        await expect(page.locator('#indexer-paused-status')).not.toHaveClass(/visible/);
        await expect(page.locator('#svc-badge-prowlarr')).toBeHidden();
    });

    test('indexer names render as text, never markup', async ({ page }) => {
        await mockStatus(page, {
            paused: [{ id: 9, name: '<img src=x onerror=alert(1)>', disabledTill: inOneHour() }],
        });
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);

        await expect(page.locator('#indexer-paused-status')).toHaveClass(/visible/);
        await expect(page.locator('#indexer-paused-msg')).toContainText('<img');
        await expect(page.locator('#indexer-paused-msg img')).toHaveCount(0);
    });
});
