// tests/playwright/specs/format-helpers.spec.js
// Pins the regression: formatSize/formatSpeed/formatETA must be on window
// and the host-disk readout (#s-space) must be non-empty after sidebar loads.
'use strict';

const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

test.describe('format helpers on window', () => {
    test('window.formatSize is a function', async ({ page }) => {
        await ensureLoggedIn(page);
        const isFunction = await page.evaluate(() => typeof window.formatSize === 'function');
        expect(isFunction).toBe(true);
    });

    test('window.formatSize(1048576) returns a non-empty MB string', async ({ page }) => {
        await ensureLoggedIn(page);
        const result = await page.evaluate(() => window.formatSize(1048576));
        expect(result).toMatch(/MB$/);
    });

    test('window.formatSpeed is a function', async ({ page }) => {
        await ensureLoggedIn(page);
        const isFunction = await page.evaluate(() => typeof window.formatSpeed === 'function');
        expect(isFunction).toBe(true);
    });

    test('window.formatETA is a function', async ({ page }) => {
        await ensureLoggedIn(page);
        const isFunction = await page.evaluate(() => typeof window.formatETA === 'function');
        expect(isFunction).toBe(true);
    });

    test('#s-space contains a non-empty size segment after sidebar loads', async ({ page }) => {
        await ensureLoggedIn(page);
        // Wait for checkHost to populate #s-space; it runs as part of refresh().
        await expect(page.locator('#s-space')).not.toBeEmpty({ timeout: 15_000 });
        const text = await page.locator('#s-space').innerText();
        expect(text).toMatch(/[\d.]+\s*(B|KB|MB|GB|TB)/);
    });
});
