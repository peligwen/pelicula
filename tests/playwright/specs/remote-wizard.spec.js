// tests/playwright/specs/remote-wizard.spec.js
//
// Tests the remote access (Peligrosa) settings drawer in the dashboard UI.
//
// NOTE: The remote config is rendered server-side by the Go CLI (RenderRemoteConfigs)
// and does not have a standalone web wizard UI that can be driven interactively
// without a running stack. The settings drawer at #st-remote-drawer is the only
// front-end surface; it reads/writes the middleware settings API.
//
// This spec:
//   1. Mocks the settings API so the drawer can open without a real backend.
//   2. Asserts the settings page loads without JS errors.
//   3. Asserts the remote-access drawer element exists in the DOM.
//
// A full remote-vhost configuration test (including cert provisioning, nginx
// template rendering, and docker-compose overlay) is covered by the Go unit
// tests in cmd/pelicula/remote_test.go.
const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

test.describe('Remote access settings (Peligrosa)', () => {
    test.beforeEach(async ({ page }) => {
        // Mock the settings read endpoint so the drawer renders without a live
        // middleware that has remote access configured.
        await page.route('**/api/pelicula/settings**', async (route) => {
            await route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({
                    remote_access_enabled: false,
                    remote_hostname: '',
                    remote_cert_mode: 'self-signed',
                }),
            });
        });
    });

    test('settings page loads without JS errors', async ({ page }) => {
        const errors = [];
        page.on('pageerror', err => errors.push(err.message));

        await ensureLoggedIn(page);
        await page.click('[data-tab="settings"]');
        await page.waitForFunction(() => document.body.dataset.tab === 'settings', { timeout: 5_000 });

        if (errors.length > 0) {
            throw new Error(`JS errors on settings tab: ${errors.join('; ')}`);
        }
    });

    test('remote access drawer element is present in DOM', async ({ page }) => {
        await ensureLoggedIn(page);

        // The #st-remote-drawer element is always in the DOM (hidden by default).
        const drawer = page.locator('#st-remote-drawer');
        await expect(drawer).toBeAttached();
    });

    test('clicking Configure opens the remote access drawer', async ({ page }) => {
        await ensureLoggedIn(page);
        await page.click('[data-tab="settings"]');
        await page.waitForFunction(() => document.body.dataset.tab === 'settings', { timeout: 5_000 });

        // The "Configure" button for remote access.
        const configureBtn = page.locator('[data-drawer-btn="remote"]');
        await expect(configureBtn).toBeVisible({ timeout: 5_000 });
        await configureBtn.click();

        const drawer = page.locator('#st-remote-drawer');
        await expect(drawer).not.toHaveClass(/hidden/, { timeout: 3_000 });
    });
});
