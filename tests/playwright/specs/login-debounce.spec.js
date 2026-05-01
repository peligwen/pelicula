// tests/playwright/specs/login-debounce.spec.js
// Pins the regression: the login submit button must be disabled while a login
// request is in-flight, preventing duplicate POSTs on rapid double-click.
'use strict';

const { test, expect } = require('@playwright/test');

test.describe('doLogin submit-button debounce', () => {
    test('only one POST to the login endpoint fires on rapid double-click', async ({ page }) => {
        // Start from a logged-out state.
        await page.context().clearCookies();
        await page.goto('/');

        // Wait for the login overlay to be visible (auth check complete).
        await expect(page.locator('[data-testid="login-overlay"]')).toBeVisible({ timeout: 15_000 });

        // Count login POSTs.
        let loginPostCount = 0;

        // Hang every login request so the button stays disabled between clicks.
        await page.route('**/api/pelicula/auth/login', route => new Promise(() => {}));

        page.on('request', req => {
            if (req.url().includes('/api/pelicula/auth/login') && req.method() === 'POST') {
                loginPostCount++;
            }
        });

        await page.fill('[data-testid="login-username"]', 'admin');
        await page.fill('[data-testid="login-password"]', 'wrongpassword');

        const submitBtn = page.locator('[data-testid="login-form"] button[type=submit]');

        // Click once to start the in-flight request.
        await submitBtn.click();

        // Verify button is now disabled (submit-disable fix).
        await expect(submitBtn).toBeDisabled({ timeout: 2_000 });

        // A second click on a disabled button should be a no-op.
        await submitBtn.click({ force: false }).catch(() => { /* may throw on disabled */ });

        // Wait a moment to catch any stray second request.
        await page.waitForTimeout(1000);

        expect(loginPostCount).toBe(1);
    });
});
