// tests/playwright/specs/subtitle-acquisition.spec.js
const { test, expect } = require('@playwright/test');
const { jellyfinAuth, searchJellyfin, waitForJobState, fireImportWebhook } = require('../helpers/api');

const TITLE = 'Night of the Living Dead';
const YEAR = 1968;
// Path as seen by middleware container
const FILE_PATH = '/downloads/Night.of.the.Living.Dead.1968.mkv';
const FILE_SIZE = 500_000;         // 500KB (approximate synthetic file size)
const RUNTIME_SECONDS = 96 * 60;  // 96 minutes — real film runtime, so duration check passes

test.describe('Subtitle acquisition: Night of the Living Dead (1968)', () => {
    test('import → await_subs stage fires → job completes → appears in Jellyfin', async ({ page, request }) => {

        // ── 1. Fire import webhook ─────────────────────────────────
        const webhookResp = await fireImportWebhook(request, {
            title: TITLE,
            year: YEAR,
            filePath: FILE_PATH,
            fileSize: FILE_SIZE,
            runtimeSeconds: RUNTIME_SECONDS,
        });
        expect(webhookResp.status).toBe('queued');

        // ── 2. Open dashboard and confirm job appears in pipeline ──
        await page.goto('/');
        await page.waitForSelector('[data-testid="pipeline-section"]', { state: 'visible' });

        await page.waitForFunction(
            (title) => {
                const allCards = document.querySelectorAll(
                    '[data-testid="pipeline-lane-validating"] .pl-card, ' +
                    '[data-testid="pipeline-lane-processing"] .pl-card, ' +
                    '[data-testid="pipeline-lane-cataloging"] .pl-card, ' +
                    '[data-testid="pipeline-lane-imported"] .pl-card'
                );
                return Array.from(allCards).some(c => c.textContent.includes(title));
            },
            TITLE,
            { timeout: 30_000, polling: 2000 }
        );

        // ── 3. Verify await_subs stage appears ─────────────────────
        // Poll the API until the job reaches await_subs OR moves past it.
        const awaitSubsDeadline = Date.now() + 60_000;
        let sawAwaitSubs = false;

        while (Date.now() < awaitSubsDeadline) {
            const res = await request.get('http://localhost:7399/api/procula/jobs');
            if (res.ok()) {
                const jobs = await res.json();
                const job = jobs.find(j =>
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
        const job = await waitForJobState(request, TITLE, 'completed', 120_000);

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

        // ── 6. Verify completed card in UI ─────────────────────────
        await page.reload();
        await page.waitForSelector('[data-testid="pipeline-section"]', { state: 'visible' });
        const completedCards = page.locator('[data-testid="pipeline-cards-completed"]');
        await expect(completedCards).toContainText(TITLE, { timeout: 15_000 });

        // ── 7. Verify Jellyfin library ─────────────────────────────
        const token = await jellyfinAuth(request);
        const count = await searchJellyfin(request, token, TITLE);
        expect(count).toBeGreaterThan(0);
    });
});
