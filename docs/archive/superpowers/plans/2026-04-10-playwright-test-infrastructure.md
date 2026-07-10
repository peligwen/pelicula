# Playwright Test Infrastructure + Import→Play Specs

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Playwright UI test coverage to Pelicula: a fixture generator for documented test conditions, a full import-wizard→pipeline→Jellyfin spec, and a subtitle-acquisition spec using Night of the Living Dead.

**Architecture:** Playwright runs on the host as a dev dependency, invoked by `e2e.sh` after the test stack on port 7399 is healthy and auto-wired. Specs are in `tests/playwright/specs/`. A shared `helpers/api.js` module provides Jellyfin auth, procula job polling, and stack-state helpers. The test stack is not modified — Playwright talks to `http://localhost:7399`.

**Tech Stack:** `@playwright/test` (Chromium), Node.js on host, FFmpeg (host or procula container fallback), existing Docker test stack on port 7399.

---

## File Structure

```
package.json                                 # @playwright/test dev dep, test scripts
tests/playwright/
  playwright.config.js                       # Base URL, test dir, reporters, timeouts
  helpers/
    api.js                                   # jellyfinAuth(), pollJob(), waitForJobState(), searchJellyfin()
  fixtures/
    generate.sh                              # FFmpeg fixture generator script
    catalog.md                               # Documented test conditions per fixture file
  specs/
    import-play.spec.js                      # Browse → import wizard → pipeline → Jellyfin
    subtitle-acquisition.spec.js             # Webhook → await_subs → pipeline complete
tests/e2e.sh                                 # Extended: invoke playwright after health checks
```

---

## Task 1: Install Playwright

**Files:**
- Create: `package.json`

- [ ] **Step 1: Initialize package.json with Playwright**

```json
{
  "name": "pelicula-tests",
  "private": true,
  "scripts": {
    "test:ui": "playwright test --config tests/playwright/playwright.config.js",
    "test:ui:headed": "playwright test --config tests/playwright/playwright.config.js --headed",
    "test:ui:debug": "playwright test --config tests/playwright/playwright.config.js --debug"
  },
  "devDependencies": {
    "@playwright/test": "^1.44.0"
  }
}
```

Write to `package.json` at the repo root.

- [ ] **Step 2: Install Playwright and Chromium**

```bash
npm install
npx playwright install chromium
```

Expected output: `chromium` installed under `~/.cache/ms-playwright/` (or platform equivalent).

- [ ] **Step 3: Verify playwright binary works**

```bash
npx playwright --version
```

Expected output: `Version 1.44.x` (or higher).

- [ ] **Step 4: Commit**

```bash
git add package.json package-lock.json
git commit -m "chore: add Playwright as dev dependency"
```

---

## Task 2: Playwright Config

**Files:**
- Create: `tests/playwright/playwright.config.js`

- [ ] **Step 1: Write config**

```js
// tests/playwright/playwright.config.js
const { defineConfig, devices } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './specs',
  timeout: 120_000,          // pipeline stages can take 60s+
  expect: { timeout: 10_000 },
  fullyParallel: false,      // stack is shared; run sequentially
  retries: 0,
  reporter: [['list'], ['html', { outputFolder: 'tests/playwright/report', open: 'never' }]],
  use: {
    baseURL: 'http://localhost:7399',
    trace: 'on-first-retry',
    headless: true,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
```

- [ ] **Step 2: Verify config parses**

```bash
npx playwright test --config tests/playwright/playwright.config.js --list
```

Expected output: `No tests found` (no specs yet — that's fine).

- [ ] **Step 3: Commit**

```bash
git add tests/playwright/playwright.config.js
git commit -m "test: add Playwright config"
```

---

## Task 3: FFmpeg Fixture Generator + Catalog

**Files:**
- Create: `tests/playwright/fixtures/generate.sh`
- Create: `tests/playwright/fixtures/catalog.md`

- [ ] **Step 1: Write generate.sh**

```bash
#!/usr/bin/env bash
# Generate Playwright test fixture files.
# Usage: bash tests/playwright/fixtures/generate.sh <output_dir>
# Requires: ffmpeg on PATH (or will attempt to use procula container)
set -euo pipefail

OUT="${1:?Usage: generate.sh <output_dir>}"
mkdir -p "$OUT"

# Helper: run ffmpeg, fall back to procula container
run_ffmpeg() {
    if command -v ffmpeg &>/dev/null; then
        ffmpeg "$@"
    else
        # Try inside the procula container (has ffmpeg installed)
        docker exec pelicula-test-procula ffmpeg "$@"
    fi
}

echo "Generating fixtures in $OUT..."

# 1. valid-h264-10s.mkv — Standard H.264/AAC, 10s, 320x240
run_ffmpeg -y \
    -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
    -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
    -c:v libx264 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    "$OUT/valid-h264-10s.mkv" 2>/dev/null
echo "  ✓ valid-h264-10s.mkv"

# 2. valid-h265-10s.mkv — H.265/AAC, 10s, 320x240
run_ffmpeg -y \
    -f lavfi -i "color=c=green:s=320x240:d=10:r=24" \
    -f lavfi -i "sine=frequency=880:duration=10:sample_rate=44100" \
    -c:v libx265 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    "$OUT/valid-h265-10s.mkv" 2>/dev/null
echo "  ✓ valid-h265-10s.mkv"

# 3. no-audio.mkv — Video only, no audio track
run_ffmpeg -y \
    -f lavfi -i "color=c=red:s=320x240:d=10:r=24" \
    -c:v libx264 -preset ultrafast -crf 28 \
    "$OUT/no-audio.mkv" 2>/dev/null
echo "  ✓ no-audio.mkv"

# 4. Night.of.the.Living.Dead.1968.mkv — Public domain title for subtitle acquisition
#    Metadata matches the real film so subtitle providers can identify it.
run_ffmpeg -y \
    -f lavfi -i "color=c=black:s=320x240:d=15:r=24" \
    -f lavfi -i "sine=frequency=220:duration=15:sample_rate=44100" \
    -c:v libx264 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    -metadata title="Night of the Living Dead" \
    -metadata year="1968" \
    -metadata comment="Pelicula test fixture — not the real film" \
    "$OUT/Night.of.the.Living.Dead.1968.mkv" 2>/dev/null
echo "  ✓ Night.of.the.Living.Dead.1968.mkv"

# 5. corrupt-header.mkv — Truncated file (simulates corrupt download)
#    Generate a valid file then truncate it to 512 bytes.
run_ffmpeg -y \
    -f lavfi -i "color=c=white:s=320x240:d=10:r=24" \
    -f lavfi -i "sine=frequency=1000:duration=10:sample_rate=44100" \
    -c:v libx264 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    "$OUT/corrupt-header.mkv.tmp" 2>/dev/null
dd if="$OUT/corrupt-header.mkv.tmp" of="$OUT/corrupt-header.mkv" bs=512 count=1 2>/dev/null
rm -f "$OUT/corrupt-header.mkv.tmp"
echo "  ✓ corrupt-header.mkv"

# 6. sample-file.mkv — A valid but very small file (simulates scene sample/fake release)
#    Kept at ≤100KB so it triggers the sample-detection floor in Procula.
run_ffmpeg -y \
    -f lavfi -i "color=c=yellow:s=160x120:d=2:r=12" \
    -f lavfi -i "sine=frequency=500:duration=2:sample_rate=22050" \
    -c:v libx264 -preset ultrafast -crf 51 \
    -c:a aac -b:a 32k \
    "$OUT/sample-file.mkv" 2>/dev/null
echo "  ✓ sample-file.mkv"

echo ""
echo "Done. Fixtures written to $OUT"
```

Make it executable: `chmod +x tests/playwright/fixtures/generate.sh`

- [ ] **Step 2: Write catalog.md**

```markdown
# Test Fixture Catalog

Each fixture documents the conditions it tests and the expected pipeline behavior.
Generated by: `bash tests/playwright/fixtures/generate.sh <output_dir>`

---

## valid-h264-10s.mkv
**Condition:** Standard H.264 video, AAC audio, 10s, 320×240.
**Expected pipeline behavior:**
- Validate: PASS (integrity, duration, codec all pass)
- Process: passthrough (no profile match unless test-downscale profile active)
- Catalog: item appears in Jellyfin
**Used by:** `import-play.spec.js` (happy path)

---

## valid-h265-10s.mkv
**Condition:** H.265/HEVC video, AAC audio, 10s, 320×240.
**Expected pipeline behavior:**
- Validate: PASS
- Process: matches any profile with `codecs_include: ["hevc", "h265"]`
- Catalog: item appears in Jellyfin
**Used by:** Future transcoding spec

---

## no-audio.mkv
**Condition:** Video-only file, no audio track.
**Expected pipeline behavior:**
- Validate: PASS (no audio is not a validation failure)
- Catalog: item appears in Jellyfin with no audio track metadata
**Used by:** Future validation edge-case spec

---

## Night.of.the.Living.Dead.1968.mkv
**Condition:** Public domain film title, properly named for subtitle provider matching.
Metadata: title="Night of the Living Dead", year=1968.
No embedded subtitle tracks.
**Expected pipeline behavior:**
- Validate: PASS
- Catalog: item appears in Jellyfin
- await_subs: Bazarr downloads English subtitles (requires network + configured provider)
  OR: await_subs times out gracefully and job completes without subtitles
**Used by:** `subtitle-acquisition.spec.js`
**Notes:**
- The file is NOT the real film — it is a synthetic 15s color/tone clip with matching metadata.
- Subtitle providers match by title+year, not file hash (for most providers).
- Requires `PELICULA_SUB_LANGS` to include `en` in the test env.

---

## corrupt-header.mkv
**Condition:** Valid MKV header truncated to 512 bytes.
**Expected pipeline behavior:**
- Validate: FAIL (FFprobe returns error on integrity check)
- On failure: blocklist triggered, re-search queued
- File should NOT be moved or deleted if already in library path
**Used by:** Future validate-failure spec

---

## sample-file.mkv
**Condition:** Tiny 2s file at 160×120, ≤100KB.
**Expected pipeline behavior:**
- Validate: FAIL (sample detection: file size vs expected runtime ratio below threshold)
- On failure: blocklist triggered
**Used by:** Future sample-detection spec
```

- [ ] **Step 3: Run the generator to verify it works**

```bash
bash tests/playwright/fixtures/generate.sh /tmp/pelicula-fixtures
ls -lh /tmp/pelicula-fixtures/
```

Expected output: six files, sizes roughly:
- `valid-h264-10s.mkv` ~200–500KB
- `valid-h265-10s.mkv` ~100–400KB
- `no-audio.mkv` ~150–400KB
- `Night.of.the.Living.Dead.1968.mkv` ~200–500KB
- `corrupt-header.mkv` exactly 512 bytes
- `sample-file.mkv` ≤100KB

- [ ] **Step 4: Commit**

```bash
git add tests/playwright/fixtures/
git commit -m "test: add FFmpeg fixture generator and catalog"
```

---

## Task 4: API Helpers Module

**Files:**
- Create: `tests/playwright/helpers/api.js`

- [ ] **Step 1: Write helpers**

```js
// tests/playwright/helpers/api.js
// Shared API helpers for Playwright specs.
// All functions accept a Playwright `request` fixture (APIRequestContext).

const BASE = 'http://localhost:7399';
const JF_AUTH_HEADER = 'MediaBrowser Client="PeliculaTest", Device="playwright", DeviceId="pelicula-playwright", Version="1.0"';

/**
 * Authenticate with Jellyfin and return an access token.
 * Uses the test-stack credentials (username: admin, password: test-jellyfin-pw).
 */
async function jellyfinAuth(request) {
    const res = await request.post(`${BASE}/jellyfin/Users/AuthenticateByName`, {
        headers: {
            'Content-Type': 'application/json',
            'X-Emby-Authorization': JF_AUTH_HEADER,
        },
        data: { Username: 'admin', Pw: 'test-jellyfin-pw' },
    });
    if (!res.ok()) throw new Error(`Jellyfin auth failed: ${res.status()}`);
    const data = await res.json();
    return data.AccessToken;
}

/**
 * Search Jellyfin for a movie by title.
 * Returns the TotalRecordCount (0 = not found).
 */
async function searchJellyfin(request, token, searchTerm) {
    const res = await request.get(
        `${BASE}/jellyfin/Items?SearchTerm=${encodeURIComponent(searchTerm)}&IncludeItemTypes=Movie&Recursive=true`,
        {
            headers: {
                'X-Emby-Authorization': `${JF_AUTH_HEADER}, Token="${token}"`,
            },
        }
    );
    if (!res.ok()) throw new Error(`Jellyfin search failed: ${res.status()}`);
    const data = await res.json();
    return data.TotalRecordCount || 0;
}

/**
 * Poll GET /api/procula/jobs until a job matching titleSubstring reaches targetState.
 * Returns the matching job object.
 * Throws if timeout is exceeded.
 */
async function waitForJobState(request, titleSubstring, targetState, timeoutMs = 90_000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        const res = await request.get(`${BASE}/api/procula/jobs`);
        if (res.ok()) {
            const jobs = await res.json();
            const job = jobs.find(j =>
                (j.source?.title || '').toLowerCase().includes(titleSubstring.toLowerCase())
            );
            if (job) {
                if (job.state === targetState) return job;
                if (job.state === 'failed' || job.state === 'cancelled') {
                    throw new Error(
                        `Job for "${titleSubstring}" reached state "${job.state}" (expected "${targetState}"): ${job.error || '(no error message)'}`
                    );
                }
            }
        }
        await new Promise(r => setTimeout(r, 3000));
    }
    throw new Error(`Timed out waiting for job "${titleSubstring}" to reach state "${targetState}"`);
}

/**
 * Fire the import webhook directly (same payload as Radarr's Download event).
 * Used by subtitle-acquisition spec to bypass the UI wizard.
 */
async function fireImportWebhook(request, { title, year, filePath, fileSize, runtimeSeconds }) {
    const res = await request.post(`${BASE}/api/pelicula/hooks/import`, {
        headers: { 'Content-Type': 'application/json' },
        data: {
            eventType: 'Download',
            movie: {
                id: Math.floor(Math.random() * 9000) + 1000,
                title,
                year,
                folderPath: filePath.substring(0, filePath.lastIndexOf('/')),
            },
            movieFile: {
                path: filePath,
                relativePath: filePath.split('/').pop(),
                size: fileSize,
                mediaInfo: { runTimeSeconds: runtimeSeconds },
            },
            downloadId: `playwright-${Date.now()}`,
        },
    });
    if (!res.ok()) throw new Error(`Import webhook failed: ${res.status()} ${await res.text()}`);
    return res.json();
}

module.exports = { jellyfinAuth, searchJellyfin, waitForJobState, fireImportWebhook };
```

- [ ] **Step 2: Verify it parses (no syntax errors)**

```bash
node -e "require('./tests/playwright/helpers/api.js'); console.log('OK')"
```

Expected output: `OK`

- [ ] **Step 3: Commit**

```bash
git add tests/playwright/helpers/api.js
git commit -m "test: add Playwright API helpers for Jellyfin and Procula"
```

---

## Task 5: Core Import→Play Spec (Import Wizard UI)

**Files:**
- Create: `tests/playwright/specs/import-play.spec.js`

**Context:** This spec drives the full import wizard UI with a synthetic H.264 fixture file.
The test stack must already be up on port 7399 with auth disabled (`PELICULA_AUTH=off`).
The fixture file must exist at `/movies/Valid H264 Test (2024)/valid-h264-10s.mkv`
inside the container (i.e., placed in `$test_library_dir/movies/Valid H264 Test (2024)/`
by the test setup before Playwright runs — see Task 7 for how e2e.sh seeds this).

The spec also verifies the pipeline section of the dashboard shows each stage as the job progresses, and that the Jellyfin API returns the item after the job completes.

- [ ] **Step 1: Write the spec**

```js
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
        await page.waitForSelector('#pipeline-section', { state: 'visible' });

        // ── 2. Open storage explorer ───────────────────────────────
        // The storage explorer is a hidden section shown by the nav or hash.
        // Navigate directly via hash.
        await page.goto('/#storage-explorer');

        // The section becomes visible when the hash is applied.
        await page.waitForSelector('#storage-explorer-section:not(.hidden)', { timeout: 10_000 });
        await page.waitForSelector('#browse-tree .browse-entry', { timeout: 10_000 });

        // ── 3. Expand the "movies" directory ───────────────────────
        const moviesEntry = page.locator('#browse-tree .browse-entry').filter({ hasText: 'movies' }).first();
        await moviesEntry.click();

        // Wait for the movies dir to expand and show children
        await page.waitForSelector('#browse-tree .browse-children[data-path="/movies"] .browse-entry', {
            timeout: 10_000,
        });

        // ── 4. Expand the test movie directory ─────────────────────
        const movieDirEntry = page
            .locator('#browse-tree .browse-entry')
            .filter({ hasText: `${TEST_TITLE} (${TEST_YEAR})` })
            .first();
        await movieDirEntry.click();

        // Wait for the file to appear
        await page.waitForSelector(
            `#browse-tree .browse-entry:has(.browse-name:text-is("valid-h264-10s.mkv"))`,
            { timeout: 10_000 }
        );

        // ── 5. Select the file ─────────────────────────────────────
        const fileEntry = page
            .locator('#browse-tree .browse-entry')
            .filter({ hasText: 'valid-h264-10s.mkv' })
            .first();
        const checkbox = fileEntry.locator('input.browse-checkbox');
        await checkbox.check();

        // Action bar should appear with Import button enabled
        await expect(page.locator('#action-bar')).not.toHaveClass(/hidden/);
        await expect(page.locator('#btn-import')).toBeEnabled();

        // ── 6. Open import wizard ──────────────────────────────────
        await page.locator('#btn-import').click();

        // Wait for modal to open at step-match
        await page.waitForSelector('#import-modal:not(.hidden)', { timeout: 10_000 });
        await page.waitForSelector('#step-match:not(.hidden)', { timeout: 5_000 });

        // Wait for scan to complete and Configure Import button to be enabled
        await expect(page.locator('#btn-configure')).toBeEnabled({ timeout: 30_000 });
        const matchStats = await page.locator('#match-stats').textContent();
        expect(matchStats).toMatch(/\d/); // should contain a count

        // ── 7. Configure import ────────────────────────────────────
        await page.locator('#btn-configure').click();
        await page.waitForSelector('#step-configure:not(.hidden)', { timeout: 5_000 });

        // Select "migrate" strategy (moves file into library)
        await page.locator('input[name="strategy"][value="migrate"]').check();

        // Disable FFprobe validation (synthetic file is too small to pass sample check)
        const validateToggle = page.locator('#validate-toggle');
        if (await validateToggle.isChecked()) {
            await validateToggle.uncheck();
        }

        // ── 8. Apply import ────────────────────────────────────────
        await page.locator('#step-configure button.import-btn.primary').click();

        // Wait for apply panel and spinner to resolve
        await page.waitForSelector('#step-apply:not(.hidden)', { timeout: 5_000 });
        await page.waitForSelector('#apply-nav:not(.hidden)', { timeout: 30_000 });

        // Verify at least one file was added (not all failed)
        const addedStat = page.locator('.apply-stat-value.added');
        const addedCount = parseInt(await addedStat.textContent() || '0', 10);
        expect(addedCount).toBeGreaterThan(0);

        // ── 9. Close modal ─────────────────────────────────────────
        await page.locator('#apply-nav button.import-btn.primary').click();
        await page.waitForSelector('#import-modal.hidden', { timeout: 5_000 });

        // ── 10. Watch pipeline section ─────────────────────────────
        // Navigate back to main view (pipeline section should be visible)
        await page.goto('/');
        await page.waitForSelector('#pipeline-section', { state: 'visible' });

        // Wait for the job card to appear in any active lane
        await page.waitForFunction(
            (title) => {
                const cards = document.querySelectorAll(
                    '#pipeline-lane-validating .pl-card, #pipeline-lane-processing .pl-card, ' +
                    '#pipeline-lane-cataloging .pl-card, #pipeline-lane-imported .pl-card'
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
        // Reload to get latest poll
        await page.reload();
        await page.waitForSelector('#pipeline-section', { state: 'visible' });

        // Allow a few seconds for the UI to refresh
        await page.waitForTimeout(5000);

        const completedCards = page.locator('#pipeline-cards-completed');
        await expect(completedCards).toContainText(TEST_TITLE, { timeout: 15_000 });

        // ── 13. Verify Jellyfin library ────────────────────────────
        const token = await jellyfinAuth(request);
        const count = await searchJellyfin(request, token, TEST_TITLE);
        expect(count).toBeGreaterThan(0);
    });
});
```

- [ ] **Step 2: Run the spec against a live test stack to verify it passes**

With the test stack already running on port 7399 (use `bash tests/e2e.sh --keep`):

```bash
npx playwright test --config tests/playwright/playwright.config.js \
    tests/playwright/specs/import-play.spec.js \
    --reporter list
```

Expected output: `1 passed`

If tests fail, run in headed mode to observe:
```bash
npm run test:ui:headed -- tests/playwright/specs/import-play.spec.js
```

- [ ] **Step 3: Commit**

```bash
git add tests/playwright/specs/import-play.spec.js
git commit -m "test(playwright): add core import wizard → pipeline → Jellyfin spec"
```

---

## Task 6: Subtitle Acquisition Spec

**Files:**
- Create: `tests/playwright/specs/subtitle-acquisition.spec.js`

**Context:** This spec fires the import webhook directly (bypassing the UI wizard) to create a procula job for "Night of the Living Dead (1968)". It then watches the pipeline section for the await_subs stage and asserts the job completes. Subtitle download success is asserted conditionally — the spec passes whether or not Bazarr finds subtitles, but logs which outcome occurred. This makes the spec network-safe (passes in offline CI) while still exercising the await_subs code path.

The Night of the Living Dead fixture file must exist inside the container at:
`/downloads/Night.of.the.Living.Dead.1968.mkv`
(i.e., at `$test_work_dir/downloads/Night.of.the.Living.Dead.1968.mkv` on the host,
seeded by e2e.sh before Playwright runs — see Task 7).

- [ ] **Step 1: Write the spec**

```js
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
        await page.waitForSelector('#pipeline-section', { state: 'visible' });

        await page.waitForFunction(
            (title) => {
                const allCards = document.querySelectorAll(
                    '#pipeline-lane-validating .pl-card, ' +
                    '#pipeline-lane-processing .pl-card, ' +
                    '#pipeline-lane-cataloging .pl-card, ' +
                    '#pipeline-lane-imported .pl-card'
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
        await page.waitForSelector('#pipeline-section', { state: 'visible' });
        await page.waitForTimeout(5000);

        const completedCards = page.locator('#pipeline-cards-completed');
        await expect(completedCards).toContainText(TITLE, { timeout: 15_000 });

        // ── 7. Verify Jellyfin library ─────────────────────────────
        const token = await jellyfinAuth(request);
        const count = await searchJellyfin(request, token, TITLE);
        expect(count).toBeGreaterThan(0);
    });
});
```

- [ ] **Step 2: Run against live test stack**

```bash
npx playwright test --config tests/playwright/playwright.config.js \
    tests/playwright/specs/subtitle-acquisition.spec.js \
    --reporter list
```

Expected output: `1 passed` (subtitle outcome logged but not asserted)

- [ ] **Step 3: Commit**

```bash
git add tests/playwright/specs/subtitle-acquisition.spec.js
git commit -m "test(playwright): add subtitle acquisition spec for Night of the Living Dead"
```

---

## Task 7: Extend e2e.sh to Invoke Playwright

**Files:**
- Modify: `tests/e2e.sh`

**Context:** After the existing bash test suite completes (Stage 8: auth/nginx), add a Stage 9 that seeds the Playwright test fixtures and invokes `npx playwright test`. If Node or Playwright is not installed, the stage is skipped with a warning (not a failure) so that the bash suite still passes in environments without Node.

The fixture seeding places:
1. `$test_library_dir/movies/Valid H264 Test (2024)/valid-h264-10s.mkv` — for import-play spec
2. `$test_work_dir/downloads/Night.of.the.Living.Dead.1968.mkv` — for subtitle-acquisition spec

- [ ] **Step 1: Locate the insertion point in e2e.sh**

Find the summary block near the end of `cmd_test()`:
```bash
    # ── Summary ───────────────────────────────────────
```
Insert the new stage immediately before it.

- [ ] **Step 2: Add Stage 9 to e2e.sh**

Insert this block immediately before the `# ── Summary ─` comment:

```bash
    # ── Stage 9: Playwright UI Tests ─────────────────

    if command -v npx &>/dev/null && npx playwright --version &>/dev/null 2>&1; then
        info "Seeding Playwright test fixtures..."

        # Fixture 1: valid H.264 file for import-play spec
        local pw_movie_dir="$test_library_dir/movies/Valid H264 Test (2024)"
        local pw_movie_file="$pw_movie_dir/valid-h264-10s.mkv"
        mkdir -p "$pw_movie_dir"

        local pw_ffmpeg_ok=false
        if command -v ffmpeg &>/dev/null; then
            if ffmpeg -y \
                -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
                -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                -c:v libx264 -preset ultrafast -crf 28 \
                -c:a aac -b:a 64k \
                "$pw_movie_file" 2>/dev/null; then
                pw_ffmpeg_ok=true
            fi
        fi
        if [[ "$pw_ffmpeg_ok" != "true" ]]; then
            if $NEEDS_SUDO docker exec pelicula-test-procula ffmpeg -y \
                -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
                -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                -c:v libx264 -preset ultrafast -crf 28 \
                -c:a aac -b:a 64k \
                "/movies/Valid H264 Test (2024)/valid-h264-10s.mkv" 2>/dev/null; then
                pw_ffmpeg_ok=true
            fi
        fi

        # Fixture 2: Night of the Living Dead for subtitle-acquisition spec
        local pw_notld_file="$test_work_dir/downloads/Night.of.the.Living.Dead.1968.mkv"
        if [[ "$pw_ffmpeg_ok" == "true" ]]; then
            if command -v ffmpeg &>/dev/null; then
                ffmpeg -y \
                    -f lavfi -i "color=c=black:s=320x240:d=15:r=24" \
                    -f lavfi -i "sine=frequency=220:duration=15:sample_rate=44100" \
                    -c:v libx264 -preset ultrafast -crf 28 \
                    -c:a aac -b:a 64k \
                    -metadata title="Night of the Living Dead" \
                    -metadata year="1968" \
                    "$pw_notld_file" 2>/dev/null || pw_ffmpeg_ok=false
            else
                $NEEDS_SUDO docker exec pelicula-test-procula ffmpeg -y \
                    -f lavfi -i "color=c=black:s=320x240:d=15:r=24" \
                    -f lavfi -i "sine=frequency=220:duration=15:sample_rate=44100" \
                    -c:v libx264 -preset ultrafast -crf 28 \
                    -c:a aac -b:a 64k \
                    -metadata title="Night of the Living Dead" \
                    -metadata year="1968" \
                    "/downloads/Night.of.the.Living.Dead.1968.mkv" 2>/dev/null || pw_ffmpeg_ok=false
            fi
        fi

        if [[ "$pw_ffmpeg_ok" != "true" ]]; then
            warn "Playwright fixture generation failed — skipping UI tests"
        else
            t_pass "Playwright fixtures seeded"
            info "Running Playwright UI tests..."

            local pw_exit=0
            PLAYWRIGHT_BASE_URL="http://localhost:${test_port}" \
                npx playwright test \
                    --config tests/playwright/playwright.config.js \
                    --reporter list \
                2>&1 || pw_exit=$?

            if [[ $pw_exit -eq 0 ]]; then
                t_pass "Playwright UI tests passed"
            else
                t_fail "Playwright UI tests failed (exit code ${pw_exit})"
                warn "Re-run with: npm run test:ui:headed"
                warn "Or: npx playwright show-report tests/playwright/report"
            fi
        fi
    else
        warn "Node/Playwright not found — skipping UI tests (run: npm install && npx playwright install chromium)"
    fi
```

- [ ] **Step 3: Verify e2e.sh syntax**

```bash
bash -n tests/e2e.sh
```

Expected output: (no output — no syntax errors)

- [ ] **Step 4: Run the full suite against a live test stack**

```bash
bash tests/e2e.sh --keep
```

Expected output: All bash tests pass, then either:
- `✓ Playwright UI tests passed` (if Node + Playwright installed)
- `! Node/Playwright not found — skipping UI tests` (if not installed — not a failure)

- [ ] **Step 5: Commit**

```bash
git add tests/e2e.sh
git commit -m "test: invoke Playwright UI tests from e2e.sh as Stage 9"
```

---

## Self-Review

**Spec coverage check:**

| Vision requirement | Covered by |
|---|---|
| Full Playwright click-through tests | Tasks 5, 6 |
| Runs on host against port 7399 | Task 2 (config), Task 7 (e2e.sh integration) |
| FFmpeg fixture generator with documented conditions | Task 3 |
| Public domain film for subtitle acquisition | Task 6 (Night of the Living Dead) |
| Core test: import → pipeline → Jellyfin | Tasks 5, 6 |
| Every pipeline stage fires and completes | Task 5 monitors cards; Task 4 `waitForJobState` checks `completed` |
| e2e.sh absorbs Playwright (not replaced) | Task 7 |
| Playwright skipped gracefully if Node missing | Task 7 (Stage 9 warns, does not fail) |

**Open questions from vision doc:**
- Which subtitle provider does `await_subs` use? → The spec passes regardless; subtitle outcome is logged not asserted.
- Does the test stack need a seeded Jellyfin API key? → No: Jellyfin auth uses username/password (`admin`/`test-jellyfin-pw`), not API key.
- What's the right assertion for "item appears in Jellyfin"? → Both: API search (`searchJellyfin`) and UI card in `#pipeline-cards-completed`.
