// tests/playwright/specs/dualsub-happy.spec.js
//
// Verifies that GenerateDualSubs produces a stacked .en-es.ass sidecar when
// both language cue sets are available as embedded SRT streams.
//
// Note: the import webhook is fired by e2e.sh Stage 9 via docker exec before
// Playwright starts, bypassing nginx's IP restriction on /api/pelicula/hooks/import.
// The fixture (Dualsub.Happy.2024.mkv) has embedded en+es SRT tracks; no Bazarr
// dependency — await_subs is skipped because MissingSubs is empty.
const fs = require('fs');
const { test, expect } = require('@playwright/test');
const { waitForJobState, ensureLoggedIn } = require('../helpers/api');
const { hostPathFor } = require('../helpers/hostfs');

const TITLE = 'Dualsub Happy';

test.describe('Dualsub: stacked ASS sidecar generation', () => {
    test('embedded en+es SRT → en-es.ass sidecar written, job completes', async ({ page }) => {

        // ── 0. Authenticate so page.request carries session cookies ──
        await ensureLoggedIn(page);

        // ── 1. Wait for job to complete ────────────────────────────
        const job = await waitForJobState(page.request, TITLE, 'completed', 120_000);

        // ── 2. Assert dualsub output ───────────────────────────────
        // dualsub_outputs must contain exactly one path ending in .en-es.ass
        const outputs = job.dualsub_outputs ?? [];
        expect(outputs.length).toBeGreaterThan(0);
        expect(outputs.some(p => p.endsWith('.en-es.ass'))).toBe(true);

        // ── 2b. Assert the actual .ass sidecar content ─────────────
        // The job record's claim is not enough (audit FT-1: "pipeline stage
        // runs; output file not asserted") — read the file the stage wrote
        // and pin the stacked-cue structure against the known fixture cues
        // seeded by e2e.sh (en: Hello/Goodbye world; es: Hola/Adios mundo).
        const assHostPath = hostPathFor(outputs.find(p => p.endsWith('.en-es.ass')));
        if (!assHostPath) {
            // Running against a stack whose library isn't on this host
            // (PELICULA_ENV_FILE unset). Under the e2e gate it is always set,
            // so the content assertions below always run there.
            test.info().annotations.push({
                type: 'skipped-check',
                description: 'PELICULA_ENV_FILE unset — .ass content not verifiable from this host',
            });
        } else {
            expect(fs.existsSync(assHostPath), `sidecar missing on disk: ${assHostPath}`).toBe(true);
            const content = fs.readFileSync(assHostPath, 'utf8');
            for (const structural of [
                '[Script Info]', '[V4+ Styles]', '[Events]',
                'Style: Top,', 'Style: Bottom,',
                'Title: Dual Subs (en-es)',
            ]) {
                expect(content, `missing ${JSON.stringify(structural)}`).toContain(structural);
            }
            // Dialogue lines: default stacked_bottom layout anchors both
            // styles bottom-center ({\an2}, stacked via MarginV); timing comes
            // from the fixture SRT cues (1–3s, 4–6s). Seconds are pinned
            // exactly; centiseconds float because ffmpeg's mkv muxing shifts
            // extracted SRT timestamps by a few cs (observed: 1.00s → 1.02s).
            for (const [style, start, end, text] of [
                ['Top',    '0:00:01', '0:00:03', 'Hola mundo'],
                ['Bottom', '0:00:01', '0:00:03', 'Hello world'],
                ['Top',    '0:00:04', '0:00:06', 'Adios mundo'],
                ['Bottom', '0:00:04', '0:00:06', 'Goodbye world'],
            ]) {
                const re = new RegExp(
                    `Dialogue: 0,${start}\\.\\d\\d,${end}\\.\\d\\d,${style},,0,0,0,,\\{\\\\an2\\}${text}`
                );
                expect(content, `missing dialogue line: ${style} ${start}→${end} "${text}"`).toMatch(re);
            }
        }

        // No subtitle languages should be missing (both langs are embedded;
        // MissingSubs is populated only during validation, which is disabled
        // for tiny test files — so missing_subs is expected to be empty here)
        expect(job.missing_subs ?? []).toHaveLength(0);

        // No dualsub error
        expect(job.dualsub_error ?? '').toBe('');

        // CatalogLate fires when DualSubOutputs is non-empty
        expect(job.catalog?.jellyfin_synced).toBe(true);

        // ── 3. Verify completed job in UI ──────────────────────────
        await page.click('[data-tab="jobs"]');
        await page.waitForSelector('#jobs-section', { state: 'visible' });
        const completedCards = page.locator('.jobs-group-completed');
        await expect(completedCards).toContainText(TITLE, { timeout: 15_000 });
    });
});
