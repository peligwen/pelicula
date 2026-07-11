// tests/playwright/specs/dualsub-failed.spec.js
//
// Verifies that when GenerateDualSubs cannot find source cues for the secondary
// language, it records dualsub_error and leaves dualsub_outputs empty — without
// failing the pipeline job. (There is no machine-translation fallback: human-
// authored subtitles or nothing.)
//
// Note: the import webhook is fired by e2e.sh Stage 9 via docker exec before
// Playwright starts. The fixture (Dualsub.Failed.2024.mkv) has only an embedded
// en SRT track. With dual_sub_pairs=["en-es"] (set in Stage 3), GenerateDualSubs
// cannot produce es cues and returns an error.
const { test, expect } = require('@playwright/test');
const { waitForJobState, ensureLoggedIn } = require('../helpers/api');

const TITLE = 'Dualsub Failed';

test.describe('Dualsub: failure path', () => {
    test('missing secondary cues → dualsub_error set, job completes', async ({ page }) => {

        // ── 0. Authenticate so page.request carries session cookies ──
        await ensureLoggedIn(page);

        // ── 1. Wait for job to complete ────────────────────────────
        const job = await waitForJobState(page.request, TITLE, 'completed', 120_000);

        // ── 2. Assert dualsub failure is recorded ──────────────────
        // No ASS sidecar should have been generated
        expect(job.dualsub_outputs ?? []).toHaveLength(0);

        // Error message must name the missing secondary language, not just
        // be non-empty — it's what the dashboard shows the admin. Both error
        // shapes qualify: `secondary cues (es): …` (extraction failed) and
        // `no subtitles found for secondary language "es"` (nothing found).
        expect(job.dualsub_error ?? '').toMatch(/secondary (cues|language)/);
        expect(job.dualsub_error ?? '').toMatch(/\(es\)|"es"/);

        // ── 3. Assert pipeline is non-fatal ───────────────────────
        expect(job.state).toBe('completed');
    });
});
