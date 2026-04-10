// tests/playwright/specs/import-play.spec.js
const { test, expect } = require('@playwright/test');
const { jellyfinAuth, searchJellyfin, waitForJobState } = require('../helpers/api');

// File placed in the library by e2e.sh before Playwright runs.
// Path is as seen by the middleware container (/movies = $LIBRARY_DIR/movies).
const TEST_TITLE = 'Valid H264 Test';
const TEST_YEAR = 2024;

test.describe('Import wizard → pipeline → Jellyfin', () => {
    test('happy path: drive import wizard, watch pipeline, verify Jellyfin', async ({ page, request }) => {

        // ── 1. Open dashboard ──────────────────────────────────────
        await page.goto('/');
        await page.waitForSelector('[data-testid="pipeline-section"]', { state: 'visible' });

        // ── 2. Open storage explorer ───────────────────────────────
        await page.goto('/#storage-explorer');
        await page.waitForSelector('[data-testid="storage-explorer-section"]:not(.hidden)', { timeout: 10_000 });
        await page.waitForSelector('[data-testid="browse-tree"] .browse-entry', { timeout: 10_000 });

        // ── 3. Expand the "movies" directory ───────────────────────
        const moviesEntry = page.locator('[data-testid="browse-tree"] .browse-entry').filter({ hasText: 'movies' }).first();
        await moviesEntry.click();

        // Wait for movies children to load
        await page.waitForSelector('[data-testid="browse-tree"] .browse-children[data-path="/movies"] .browse-entry', {
            timeout: 10_000,
        });

        // ── 4. Expand the test movie directory ─────────────────────
        const movieDirEntry = page
            .locator('[data-testid="browse-tree"] .browse-entry')
            .filter({ hasText: `${TEST_TITLE} (${TEST_YEAR})` })
            .first();
        await movieDirEntry.click();

        // Wait for the file to appear
        await page.waitForFunction(
            () => {
                const entries = document.querySelectorAll('[data-testid="browse-tree"] .browse-entry');
                return Array.from(entries).some(e => e.textContent.includes('valid-h264-10s.mkv'));
            },
            { timeout: 10_000 }
        );

        // ── 5. Select the file ─────────────────────────────────────
        const fileEntry = page
            .locator('[data-testid="browse-tree"] .browse-entry')
            .filter({ hasText: 'valid-h264-10s.mkv' })
            .first();
        const checkbox = fileEntry.locator('input.browse-checkbox');
        await checkbox.check();

        // Action bar should appear
        await expect(page.locator('#action-bar')).not.toHaveClass(/hidden/);
        await expect(page.locator('[data-testid="btn-import"]')).toBeEnabled();

        // ── 6. Open import wizard ──────────────────────────────────
        await page.locator('[data-testid="btn-import"]').click();

        // Wait for modal and step-match
        await page.waitForSelector('[data-testid="import-modal"]:not(.hidden)', { timeout: 10_000 });
        await page.waitForSelector('#step-match:not(.hidden)', { timeout: 5_000 });

        // Wait for scan to complete and btn-configure to be enabled
        await expect(page.locator('[data-testid="btn-configure"]')).toBeEnabled({ timeout: 30_000 });

        // ── 7. Configure import ────────────────────────────────────
        await page.locator('[data-testid="btn-configure"]').click();
        await page.waitForSelector('#step-configure:not(.hidden)', { timeout: 5_000 });

        // Select "migrate" strategy
        await page.locator('input[name="strategy"][value="migrate"]').check();

        // Disable FFprobe validation (synthetic file is too small to pass sample check)
        const validateToggle = page.locator('#validate-toggle');
        if (await validateToggle.isChecked()) {
            await validateToggle.uncheck();
        }

        // ── 8. Apply import ────────────────────────────────────────
        await page.locator('#step-configure button.import-btn.primary').click();

        // Wait for apply panel
        await page.waitForSelector('#step-apply:not(.hidden)', { timeout: 5_000 });

        // Wait for done nav to appear (import finished)
        await page.waitForSelector('[data-testid="apply-nav"]:not(.hidden)', { timeout: 30_000 });

        // At least one file should have been added
        const addedStat = page.locator('.apply-stat-value.added');
        const addedCount = parseInt(await addedStat.textContent() || '0', 10);
        expect(addedCount).toBeGreaterThan(0);

        // ── 9. Close modal ─────────────────────────────────────────
        await page.locator('[data-testid="apply-nav"] button.import-btn.primary').click();
        await page.waitForSelector('[data-testid="import-modal"].hidden', { timeout: 5_000 });

        // ── 10. Watch pipeline section ─────────────────────────────
        await page.goto('/');
        await page.waitForSelector('[data-testid="pipeline-section"]', { state: 'visible' });

        // Wait for a job card to appear in any active pipeline lane
        await page.waitForFunction(
            (title) => {
                const cards = document.querySelectorAll(
                    '[data-testid="pipeline-lane-validating"] .pl-card, ' +
                    '[data-testid="pipeline-lane-processing"] .pl-card, ' +
                    '[data-testid="pipeline-lane-cataloging"] .pl-card, ' +
                    '[data-testid="pipeline-lane-imported"] .pl-card'
                );
                return Array.from(cards).some(c => c.textContent.includes(title));
            },
            TEST_TITLE,
            { timeout: 30_000, polling: 2000 }
        );

        // ── 11. Wait for job to complete via API ───────────────────
        const job = await waitForJobState(request, TEST_TITLE, 'completed', 90_000);
        expect(job.catalog?.jellyfin_synced).toBe(true);

        // ── 12. Verify completed card appears in UI ─────────────────
        await page.reload();
        await page.waitForSelector('[data-testid="pipeline-section"]', { state: 'visible' });
        const completedCards = page.locator('[data-testid="pipeline-cards-completed"]');
        await expect(completedCards).toContainText(TEST_TITLE, { timeout: 15_000 });

        // ── 13. Verify Jellyfin library ────────────────────────────
        const token = await jellyfinAuth(request);
        const count = await searchJellyfin(request, token, TEST_TITLE);
        expect(count).toBeGreaterThan(0);
    });
});
