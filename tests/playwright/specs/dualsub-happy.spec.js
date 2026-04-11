// tests/playwright/specs/dualsub-happy.spec.js
//
// Verifies that GenerateDualSubs produces a stacked .en-es.ass sidecar when
// both language cue sets are available as embedded SRT streams.
//
// Note: the import webhook is fired by e2e.sh Stage 9 via docker exec before
// Playwright starts, bypassing nginx's IP restriction on /api/pelicula/hooks/import.
// The fixture (Dualsub.Happy.2024.mkv) has embedded en+es SRT tracks; no Bazarr
// dependency — await_subs is skipped because MissingSubs is empty.
const { test, expect } = require('@playwright/test');
const { waitForJobState } = require('../helpers/api');

const TITLE = 'Dualsub Happy';

test.describe('Dualsub: stacked ASS sidecar generation', () => {
    test('embedded en+es SRT → en-es.ass sidecar written, job completes', async ({ page }) => {

        // ── 1. Wait for job to complete ────────────────────────────
        const job = await waitForJobState(page.request, TITLE, 'completed', 120_000);

        // ── 2. Assert dualsub output ───────────────────────────────
        // dualsub_outputs must contain exactly one path ending in .en-es.ass
        const outputs = job.dualsub_outputs ?? [];
        expect(outputs.length).toBeGreaterThan(0);
        expect(outputs.some(p => p.endsWith('.en-es.ass'))).toBe(true);

        // No subtitle languages should be missing (both langs are embedded;
        // MissingSubs is populated only during validation, which is disabled
        // for tiny test files — so missing_subs is expected to be empty here)
        expect(job.missing_subs ?? []).toHaveLength(0);

        // No dualsub error
        expect(job.dualsub_error ?? '').toBe('');

        // CatalogLate fires when DualSubOutputs is non-empty
        expect(job.catalog?.jellyfin_synced).toBe(true);

        // ── 3. Verify completed card in UI ─────────────────────────
        await page.goto('/');
        await page.click('[data-tab="coming"]');
        await page.waitForSelector('[data-testid="pipeline-section"]', { state: 'visible' });
        const completedCards = page.locator('[data-testid="pipeline-cards-completed"]');
        await expect(completedCards).toContainText(TITLE, { timeout: 15_000 });
    });
});
