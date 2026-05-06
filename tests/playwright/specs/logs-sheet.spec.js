// tests/playwright/specs/logs-sheet.spec.js
// Pins the regression: the logs-sheet refresh button must call the aggregate
// logs endpoint, and the per-service modal refresh button must NOT.
'use strict';

const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

const AGGREGATE_URL_PATTERN = '**/api/pelicula/logs/aggregate**';

test.describe('Logs sheet refresh button', () => {
    test('clicking #logs-sheet-refresh-btn fires a request to the aggregate logs endpoint', async ({ page }) => {
        await ensureLoggedIn(page);

        // The logs-sheet-panel-row lives inside #settings-section, which is hidden by
        // CSS until body[data-tab="settings"]. Default tab on dashboard load is "search".
        await page.click('[data-tab="settings"]');
        await page.waitForFunction(() => document.body.dataset.tab === 'settings', { timeout: 5_000 });

        // Open the logs sheet via its dedicated open button.
        await page.locator('#logs-sheet-open-btn').click();
        await expect(page.locator('#logs-sheet')).not.toHaveClass(/hidden/);

        // Capture the next aggregate request triggered by the refresh button.
        const [req] = await Promise.all([
            page.waitForRequest(AGGREGATE_URL_PATTERN, { timeout: 10_000 }),
            page.locator('#logs-sheet-refresh-btn').click(),
        ]);
        expect(req.url()).toContain('/api/pelicula/logs/aggregate');
    });

    test('clicking #log-refresh-btn in the per-service modal does NOT fire the aggregate logs endpoint', async ({ page }) => {
        await ensureLoggedIn(page);

        // Track any aggregate-logs requests that fire.
        const aggregateRequests = [];
        page.on('request', req => {
            if (req.url().includes('/api/pelicula/logs/aggregate')) {
                aggregateRequests.push(req);
            }
        });

        // Open the per-service log modal by clicking a service log button.
        // The svc-sidebar-list delegation calls showServiceLogs when a .svc-row-log is clicked.
        const logBtn = page.locator('#svc-sidebar-list .svc-row-log[data-svc]').first();
        await expect(logBtn).toBeVisible({ timeout: 10_000 });
        await logBtn.click();

        // Wait for the per-service log modal to become visible.
        await expect(page.locator('#log-modal')).not.toHaveClass(/hidden/, { timeout: 5_000 });

        // Reset the counter now that the modal is open (the modal open itself may
        // load per-service logs, not aggregate).
        const countBefore = aggregateRequests.length;

        // Click the per-service refresh button and wait briefly for any stray requests.
        await page.locator('#log-refresh-btn').click();
        await page.waitForTimeout(1500);

        // No new aggregate requests should have been fired.
        expect(aggregateRequests.length).toBe(countBefore);
    });
});
