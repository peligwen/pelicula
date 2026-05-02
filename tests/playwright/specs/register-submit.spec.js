// tests/playwright/specs/register-submit.spec.js
// Covers register.js hardening: AbortController/button-debounce, auto-focus,
// client-side username validation, and class-based error display.
'use strict';

const { test, expect } = require('@playwright/test');

// A valid 43-char URL-safe base64 token that passes the format check.
const VALID_TOKEN_FORMAT = 'A'.repeat(43);

test.describe('register submit hardening', () => {
    // Guard: no alert/confirm/prompt dialogs should appear on the register page.
    test.beforeEach(async ({ page }) => {
        page.on('dialog', d => {
            throw new Error(`Unexpected dialog: ${d.message()}`);
        });
    });

    test('double-click submit fires exactly one POST (button debounce)', async ({ page }) => {
        // Open-registration mode — no token in URL.
        await page.route('**/api/pelicula/register/check', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ open_registration: true }),
            });
        });

        // Hang the register POST (exact path) so the button stays disabled between clicks.
        await page.route('**/api/pelicula/register', route => {
            if (route.request().url().endsWith('/api/pelicula/register')) {
                return new Promise(() => {});
            }
            route.continue();
        });

        // Stub password suggestion so the form loads cleanly.
        await page.route('**/api/pelicula/generate-password', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ password: 'TestPass1!' }),
            });
        });

        let postCount = 0;
        page.on('request', req => {
            if (req.url().includes('/api/pelicula/register') &&
                !req.url().includes('/check') &&
                req.method() === 'POST') {
                postCount++;
            }
        });

        await page.goto('/register');
        await page.waitForSelector('#reg-form-wrap', { state: 'visible', timeout: 10_000 });

        await page.fill('#reg-username', 'testuser');
        await page.fill('#reg-password', 'TestPass1!');
        await page.fill('#reg-confirm', 'TestPass1!');

        const submitBtn = page.locator('#reg-submit');

        // First click — starts the in-flight request.
        await submitBtn.click();

        // Button must be disabled while request is pending.
        await expect(submitBtn).toBeDisabled({ timeout: 2_000 });

        // Second click on a disabled button is a no-op.
        await submitBtn.click({ force: false }).catch(() => {});

        await page.waitForTimeout(500);
        expect(postCount).toBe(1);
    });

    test('invalid token renders dead-state without autofocus crash', async ({ page }) => {
        // Intercept the token check and return 404/invalid.
        await page.route(`**/api/pelicula/invites/${VALID_TOKEN_FORMAT}/check`, route => {
            route.fulfill({
                status: 404,
                contentType: 'application/json',
                body: JSON.stringify({ state: 'not_found' }),
            });
        });

        const errors = [];
        page.on('pageerror', e => errors.push(String(e)));

        await page.goto(`/register?t=${VALID_TOKEN_FORMAT}`);
        await page.waitForSelector('#reg-dead', { state: 'visible', timeout: 10_000 });

        expect(await page.locator('#reg-form-wrap').isVisible()).toBe(false);
        expect(errors, 'no JS errors on dead state').toEqual([]);
    });

    test('open-registration mode auto-focuses reg-username on load', async ({ page }) => {
        await page.route('**/api/pelicula/register/check', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ open_registration: true }),
            });
        });
        await page.route('**/api/pelicula/generate-password', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ password: 'TestPass1!' }),
            });
        });

        await page.goto('/register');
        await page.waitForSelector('#reg-form-wrap', { state: 'visible', timeout: 10_000 });

        const isFocused = await page.locator('#reg-username').evaluate(
            el => el === document.activeElement
        );
        expect(isFocused, 'reg-username must have focus after open-reg form shows').toBe(true);
    });

    test('IsValidUsername-violating inputs show inline error without network call', async ({ page }) => {
        await page.route('**/api/pelicula/register/check', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ open_registration: true }),
            });
        });
        await page.route('**/api/pelicula/generate-password', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ password: 'TestPass1!' }),
            });
        });

        let networkCallCount = 0;
        await page.route('**/api/pelicula/register', route => {
            if (route.request().url().endsWith('/api/pelicula/register')) {
                networkCallCount++;
                route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
            } else {
                route.continue();
            }
        });

        await page.goto('/register');
        await page.waitForSelector('#reg-form-wrap', { state: 'visible', timeout: 10_000 });

        const invalidUsernames = ['foo/bar', 'foo\\bar', 'foo bar  '];
        for (const bad of invalidUsernames) {
            await page.fill('#reg-username', bad);
            await page.fill('#reg-password', 'TestPass1!');
            await page.fill('#reg-confirm', 'TestPass1!');
            await page.locator('#reg-submit').click();
            await expect(page.locator('#reg-error'), `error shown for username: ${bad}`).toBeVisible({ timeout: 2_000 });
        }

        expect(networkCallCount, 'no network calls for invalid usernames').toBe(0);
    });

    test('client-side error uses class toggle, not inline style', async ({ page }) => {
        await page.route('**/api/pelicula/register/check', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ open_registration: true }),
            });
        });
        await page.route('**/api/pelicula/generate-password', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ password: 'TestPass1!' }),
            });
        });

        await page.goto('/register');
        await page.waitForSelector('#reg-form-wrap', { state: 'visible', timeout: 10_000 });

        // Trigger a validation error with an empty username.
        await page.fill('#reg-username', '');
        await page.locator('#reg-submit').click();

        const errEl = page.locator('#reg-error');
        await expect(errEl).toBeVisible({ timeout: 2_000 });

        // Must use class-based visibility, not inline style.
        const hasShowClass = await errEl.evaluate(el => el.classList.contains('show'));
        expect(hasShowClass, 'reg-error visibility must be class-based (.show)').toBe(true);

        const inlineDisplay = await errEl.evaluate(el => el.style.display);
        expect(inlineDisplay, 'reg-error must not use inline style.display').toBe('');
    });
});
