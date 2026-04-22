// tests/playwright/specs/network-drawer.spec.js
//
// Verifies that the bandwidth stats drawer opens and renders container rows
// by mocking GET /api/pelicula/network. No real Docker or middleware needed.
const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

const MOCK_NETWORK_RESPONSE = {
    containers: [
        { name: 'sonarr',      bytes_in: 1024, bytes_out: 512,  vpn_routed: false },
        { name: 'qbittorrent', bytes_in: 8192, bytes_out: 4096, vpn_routed: true  },
    ],
    as_of: new Date().toISOString(),
};

test.describe('Network bandwidth drawer', () => {
    test.beforeEach(async ({ page }) => {
        // Intercept the network stats endpoint before any navigation.
        await page.route('**/api/pelicula/network', async (route) => {
            await route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify(MOCK_NETWORK_RESPONSE),
            });
        });
    });

    test('drawer opens and shows container name from mock response', async ({ page }) => {
        await ensureLoggedIn(page);

        // The bandwidth button is inside the status section header.
        const netBtn = page.locator('#net-drawer-btn');
        await expect(netBtn).toBeVisible({ timeout: 5_000 });
        await netBtn.click();

        // The drawer element should become visible.
        const drawer = page.locator('#net-drawer');
        await expect(drawer).not.toHaveClass(/hidden/, { timeout: 5_000 });

        // The drawer body should contain the mocked container name.
        const body = page.locator('#net-drawer-body');
        await expect(body).toContainText('sonarr', { timeout: 5_000 });
    });

    test('drawer shows VPN-routed container', async ({ page }) => {
        await ensureLoggedIn(page);

        const netBtn = page.locator('#net-drawer-btn');
        await netBtn.click();

        const body = page.locator('#net-drawer-body');
        await expect(body).toContainText('qbittorrent', { timeout: 5_000 });
    });
});
