// tests/playwright/specs/dualsub-failed.spec.js
//
// Verifies that when GenerateDualSubs cannot find source cues for the secondary
// language and the translator is "none", it records dualsub_error and leaves
// dualsub_outputs empty — without failing the pipeline job.
//
// Note: the import webhook is fired by e2e.sh Stage 9 via docker exec before
// Playwright starts. The fixture (Dualsub.Failed.2024.mkv) has only an embedded
// en SRT track. With dual_sub_pairs=["en-es"] and dual_sub_translator="none"
// (set in Stage 3), GenerateDualSubs cannot produce es cues and returns an error.
const { test, expect } = require('@playwright/test');
const { waitForJobState, ensureLoggedIn } = require('../helpers/api');

const TITLE = 'Dualsub Failed';

test.describe('Dualsub: failure path', () => {
    test('missing secondary cues + translator=none → dualsub_error set, job completes', async ({ page }) => {

        // ── 0. Authenticate so page.request carries session cookies ──
        await ensureLoggedIn(page);

        // ── 1. Wait for job to complete ────────────────────────────
        const job = await waitForJobState(page.request, TITLE, 'completed', 120_000);

        // ── 2. Assert dualsub failure is recorded ──────────────────
        // No ASS sidecar should have been generated
        expect(job.dualsub_outputs ?? []).toHaveLength(0);

        // Error message must describe the failure
        expect(job.dualsub_error ?? '').not.toBe('');

        // ── 3. Assert pipeline is non-fatal ───────────────────────
        expect(job.state).toBe('completed');
    });
});
