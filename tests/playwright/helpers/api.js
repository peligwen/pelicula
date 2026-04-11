// tests/playwright/helpers/api.js
// Shared API helpers for Playwright specs.
// All functions accept a Playwright `request` fixture (APIRequestContext).

const BASE = process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:7399';
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

/**
 * Navigate to the dashboard and log in if the login overlay is visible.
 * Call this before any page.request API calls that require auth.
 */
async function ensureLoggedIn(page) {
    await page.goto('/');
    const loginOverlay = page.locator('[data-testid="login-overlay"]');
    const authCheckDone = page.waitForResponse(
        r => r.url().includes('/api/pelicula/auth/check'), { timeout: 8_000 }
    ).catch(() => null);
    await authCheckDone;
    if (await loginOverlay.isVisible()) {
        await page.fill('[data-testid="login-username"]', 'admin');
        await page.fill('[data-testid="login-password"]', 'test-jellyfin-pw');
        await page.click('[data-testid="login-form"] [type=submit]');
        await loginOverlay.waitFor({ state: 'hidden', timeout: 15_000 });
    }
}

module.exports = { jellyfinAuth, searchJellyfin, waitForJobState, fireImportWebhook, ensureLoggedIn };
