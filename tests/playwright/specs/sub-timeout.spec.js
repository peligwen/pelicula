// tests/playwright/specs/sub-timeout.spec.js
//
// Verifies that when Bazarr cannot deliver subtitles within sub_acquire_timeout_min,
// the pipeline emits sub_timeout, retains missing_subs, and still completes the job.
//
// Setup (e2e.sh Stage 9):
//   • Fixture: Pelicula.Timeout.Fixture.2099.mkv — no subtitle tracks, padded to
//     64 MB so it clears the 50 MB validation sample floor.
//   • Webhook fired with validation temporarily re-enabled so checkMissingSubtitles
//     populates MissingSubs=["en"] → await_subs stage is entered.
//   • sub_acquire_timeout_min=1 (set in Stage 3) limits the Bazarr wait to 1 minute.
//   • Bazarr has no configured providers in the test stack → search always fails.
const { test, expect } = require('@playwright/test');
const { waitForJobState } = require('../helpers/api');

const TITLE = 'Pelicula Timeout Fixture';

test.describe('Subtitle acquisition: timeout path', () => {
    test('await_subs times out → missing_subs retained, job completes', async ({ page }) => {
        // Allow 3 minutes: 1 min await_subs timeout + 2 min buffer for CI variance.
        test.setTimeout(200_000);
        const job = await waitForJobState(page.request, TITLE, 'completed', 190_000);

        // Subtitles were never delivered — missing_subs must still contain 'en'
        expect(job.missing_subs ?? []).toContain('en');

        // Nothing was acquired
        expect(job.subs_acquired ?? []).toHaveLength(0);

        // Timeout is non-fatal: the job must have completed successfully
        expect(job.state).toBe('completed');
    });
});
