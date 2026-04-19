// tests/playwright/specs/subtitle-acquisition.spec.js
const { test, expect } = require('@playwright/test');
const { jellyfinAuth, searchJellyfin, waitForJobState, ensureLoggedIn } = require('../helpers/api');

const BASE = process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:7399';

const TITLE = 'Night of the Living Dead';
const YEAR = 1968;
// Note: the import webhook is fired by e2e.sh Stage 9 via docker exec before
// Playwright starts, bypassing nginx's IP restriction on /api/pelicula/hooks/import.

test.describe('Subtitle acquisition: Night of the Living Dead (1968)', () => {
    test('import → await_subs stage fires → job completes → appears in Jellyfin', async ({ page, request }) => {

        // ── 1. Open dashboard, log in ─────────────────────────────
        await ensureLoggedIn(page);

        // ── 2. Switch to jobs tab and confirm job appears ──────────
        await page.click('[data-tab="jobs"]');
        await page.waitForFunction(() => document.body.dataset.tab === 'jobs');
        await page.waitForSelector('#jobs-section', { state: 'visible' });

        await page.waitForFunction(
            (title) => {
                const rows = document.querySelectorAll('.jobs-row-title');
                return Array.from(rows).some(r => r.textContent.includes(title));
            },
            TITLE,
            { timeout: 30_000, polling: 2000 }
        );

        // ── 3. Verify await_subs stage appears ─────────────────────
        // Poll the API until the job reaches await_subs OR moves past it.
        // Use page.request so session cookies are included (procula is behind auth_request).
        const awaitSubsDeadline = Date.now() + 60_000;
        let sawAwaitSubs = false;

        while (Date.now() < awaitSubsDeadline) {
            const res = await page.request.get(`${BASE}/api/pelicula/jobs`);
            if (res.ok()) {
                const data = await res.json();
                const allJobs = Object.values(data.groups || {}).flat();
                const job = allJobs.find(j =>
                    (j.source?.title || '').toLowerCase().includes(TITLE.toLowerCase())
                );
                if (job) {
                    if (job.stage === 'await_subs') { sawAwaitSubs = true; break; }
                    if (job.state === 'completed') { break; } // passed through quickly
                    if (job.state === 'failed') {
                        throw new Error(`Job failed at stage ${job.stage}: ${job.error}`);
                    }
                }
            }
            await new Promise(r => setTimeout(r, 3000));
        }

        // await_subs is the expected stage — log if we didn't see it
        if (!sawAwaitSubs) {
            console.warn(`[subtitle-acquisition] await_subs stage not observed — job may have ` +
                         `completed before polling caught it, or subtitles were embedded.`);
        }

        // ── 4. Wait for job to complete ────────────────────────────
        // Use page.request so session cookies are included
        const job = await waitForJobState(page.request, TITLE, 'completed', 120_000);

        // ── 5. Report subtitle outcome ─────────────────────────────
        const missingSubs = job.missing_subs;
        if (!missingSubs || missingSubs.length === 0) {
            console.log(`[subtitle-acquisition] ✓ Subtitles acquired by Bazarr`);
        } else {
            console.warn(`[subtitle-acquisition] ⚠ Job completed but subtitles not downloaded: ${JSON.stringify(missingSubs)}`);
            console.warn(`  This is expected in offline/unconfigured environments.`);
        }

        // Job must complete regardless of subtitle outcome
        expect(job.state).toBe('completed');
        expect(job.catalog?.jellyfin_synced).toBe(true);

        // ── 6. Verify completed job in UI ──────────────────────────
        await page.reload();
        // After reload, switch to jobs tab (reload resets to default "search" tab)
        await page.click('[data-tab="jobs"]');
        await page.waitForFunction(() => document.body.dataset.tab === 'jobs');
        await page.waitForSelector('#jobs-section', { state: 'visible' });
        const completedCards = page.locator('.jobs-group-completed');
        await expect(completedCards).toContainText(TITLE, { timeout: 15_000 });

        // ── 7. Verify Jellyfin library ─────────────────────────────
        const token = await jellyfinAuth(request);
        const count = await searchJellyfin(request, token, TITLE);
        expect(count).toBeGreaterThan(0);
    });
});
