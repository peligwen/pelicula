// tests/playwright/specs/import-play.spec.js
const { test, expect } = require('@playwright/test');
const { jellyfinAuth, searchJellyfin, waitForJobState, ensureLoggedIn } = require('../helpers/api');

// File placed in the library by e2e.sh before Playwright runs.
// Path is as seen by the middleware container (/movies = $LIBRARY_DIR/movies).
// "Sintel" is a real TMDB title (Blender Foundation, 2010) so scan produces a match.
const TEST_TITLE = 'Sintel';
const TEST_YEAR = 2010;

test.describe('Import wizard → pipeline → Jellyfin', () => {
    test('happy path: drive import wizard, watch pipeline, verify Jellyfin', async ({ page, request }) => {

        // ── 1. Open dashboard, log in ─────────────────────────────
        await ensureLoggedIn(page);

        // ── 2. Open storage explorer ───────────────────────────────
        await page.click('[data-tab="storage"]');
        await page.click('[data-stab="explorer"]');
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
                return Array.from(entries).some(e => e.textContent.includes('Sintel.2010.mkv'));
            },
            { timeout: 10_000 }
        );

        // ── 5. Select the file ─────────────────────────────────────
        const fileEntry = page
            .locator('[data-testid="browse-tree"] .browse-entry')
            .filter({ hasText: 'Sintel.2010.mkv' })
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

        // Select "import" strategy (move files into library)
        await page.locator('input[name="strategy"][value="import"]').check();
        // Leave validate-toggle checked (default) — Procula's validation_enabled=false
        // setting (set by e2e.sh Stage 3) prevents FFprobe from running on the
        // synthetic file, but the toggle must stay on to queue a Procula job.

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
        await page.locator('[data-testid="import-modal"]').waitFor({ state: 'hidden', timeout: 5_000 });

        // ── 10. Watch jobs section ─────────────────────────────────
        await page.click('[data-tab="jobs"]');
        await page.waitForSelector('#jobs-section', { state: 'visible' });

        // Wait for a job row to appear in any state (including completed —
        // the job may finish before Playwright starts polling).
        await page.waitForFunction(
            (title) => {
                const rows = document.querySelectorAll('.jobs-row-title');
                return Array.from(rows).some(r => r.textContent.includes(title));
            },
            TEST_TITLE,
            { timeout: 30_000, polling: 2000 }
        );

        // ── 11. Wait for job to complete via API ───────────────────
        // Use page.request so session cookies are included (procula is behind auth_request)
        const job = await waitForJobState(page.request, TEST_TITLE, 'completed', 90_000);
        expect(job.catalog?.jellyfin_synced).toBe(true);

        // ── 12. Verify completed job appears in UI ──────────────────
        await page.reload();
        // After reload, session cookie is still valid — no re-login needed
        // Switch to jobs tab (reload resets to default "search" tab)
        await page.click('[data-tab="jobs"]');
        await page.waitForSelector('#jobs-section', { state: 'visible' });
        const completedCards = page.locator('.jobs-group-completed');
        await expect(completedCards).toContainText(TEST_TITLE, { timeout: 15_000 });

        // ── 13. Verify Jellyfin library ────────────────────────────
        const token = await jellyfinAuth(request);
        const count = await searchJellyfin(request, token, TEST_TITLE);
        expect(count).toBeGreaterThan(0);
    });
});
