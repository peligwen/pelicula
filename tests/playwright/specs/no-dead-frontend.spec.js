// tests/playwright/specs/no-dead-frontend.spec.js
// Guards against reintroduction of dead patterns removed in the Round 4 / Phase 3
// refactor: dead pl-* CSS, duplicate notif helpers, and inline color literals.
'use strict';

const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

test.describe('no dead frontend patterns', () => {
    test('styles.css contains no dead pl-* selectors', async ({ page }) => {
        await ensureLoggedIn(page);
        const resp = await page.request.get('/styles.css');
        expect(resp.ok()).toBe(true);
        const css = await resp.text();
        for (const dead of ['.pl-card-', '.pl-chip', '.pl-event-', '.pl-lane-', '.pl-empty', '.pl-year', '#pipeline-attention']) {
            expect(css, `styles.css must not contain dead selector: ${dead}`).not.toContain(dead);
        }
    });

    test('window.notifIcon and window.notifClass are functions', async ({ page }) => {
        await ensureLoggedIn(page);
        const iconFn  = await page.evaluate(() => typeof window.notifIcon);
        const classFn = await page.evaluate(() => typeof window.notifClass);
        expect(iconFn,  'window.notifIcon must be a function').toBe('function');
        expect(classFn, 'window.notifClass must be a function').toBe('function');
    });

    test('import.js contains no inline color:#', async ({ page }) => {
        await ensureLoggedIn(page);
        const resp = await page.request.get('/import.js');
        expect(resp.ok()).toBe(true);
        const js = await resp.text();
        expect(js, 'import.js must not contain inline style="color:#"').not.toMatch(/style="[^"]*color\s*:\s*#/);
    });

    test('users.js contains no inline color:#', async ({ page }) => {
        await ensureLoggedIn(page);
        const resp = await page.request.get('/users.js');
        expect(resp.ok()).toBe(true);
        const js = await resp.text();
        expect(js, 'users.js must not contain inline style="color:#"').not.toMatch(/style="[^"]*color\s*:\s*#/);
    });

    test('setup.html body contains no inline color/border literals', async ({ page }) => {
        await ensureLoggedIn(page);
        const resp = await page.request.get('/setup.html');
        expect(resp.ok()).toBe(true);
        const html = await resp.text();
        expect(html, 'setup.html must not contain inline style="color:#"').not.toMatch(/style="[^"]*color\s*:\s*#[0-9a-fA-F]/);
        // border check: exclude var(--x,#fallback) forms — bare #hex after a border property is the target
        expect(html, 'setup.html must not contain inline style border with bare #hex').not.toMatch(/style="[^"]*border[^"]*:\s*[^"(]*#[0-9a-fA-F]/);
    });

    test('index.html body contains no inline color/border literals (except documented exception)', async ({ page }) => {
        await ensureLoggedIn(page);
        const resp = await page.request.get('/index.html');
        expect(resp.ok()).toBe(true);
        const html = await resp.text();
        // color:#000 on dsp-update-btn is an intentional fixed foreground on --accent background — documented in HTML comment
        const colorHits = [...html.matchAll(/style="[^"]*color\s*:\s*#[0-9a-fA-F][^"]*"/g)]
            .map(m => m[0])
            .filter(s => !s.includes('color:#000'));
        expect(colorHits, 'index.html must not contain inline style="color:#" (except the documented color:#000 exception)').toHaveLength(0);
        expect(html, 'index.html must not contain inline style border with bare #hex').not.toMatch(/style="[^"]*border[^"]*:\s*[^"(]*#[0-9a-fA-F]/);
    });

    test('setup.html loads without console errors', async ({ page }) => {
        const errors = [];
        page.on('pageerror', e => errors.push(String(e)));
        await ensureLoggedIn(page);
        await page.goto('/setup.html');
        await page.waitForLoadState('networkidle');
        expect(errors, 'setup.html must load without JS errors').toEqual([]);
    });

    test('dashboard loads without console errors after inline-color reap', async ({ page }) => {
        const errors = [];
        page.on('pageerror', e => errors.push(String(e)));
        await ensureLoggedIn(page);
        await page.waitForLoadState('networkidle');
        expect(errors, 'dashboard must load without JS errors after inline-color reap').toEqual([]);
    });

    test('notification + activity rendering produces no module-scope ReferenceErrors', async ({ page }) => {
        const errors = [];
        page.on('pageerror', e => errors.push(String(e)));

        // Stub /api/pelicula/notifications to return events with populated `type`
        // fields so that notifIcon(e.type) and notifClass(e.type) are actually
        // invoked during renderNotifications() and renderActivity() calls.
        await page.route('**/api/pelicula/notifications', route => {
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify([
                    {
                        id: 'test-1',
                        type: 'download_complete',
                        message: 'Test download complete',
                        timestamp: new Date().toISOString(),
                    },
                    {
                        id: 'test-2',
                        type: 'transcode_failed',
                        message: 'Test transcode failed',
                        detail: 'codec error',
                        job_id: 'job-99',
                        timestamp: new Date(Date.now() - 48 * 60 * 60 * 1000).toISOString(),
                    },
                    {
                        id: 'test-3',
                        type: 'storage_warning',
                        message: 'Disk usage above 80%',
                        timestamp: new Date(Date.now() - 60 * 1000).toISOString(),
                    },
                ]),
            });
        });

        await ensureLoggedIn(page);

        // Wait for the activity list to render something — dashboard.js calls
        // renderActivity() after fetching notifications on its refresh cycle.
        await page.waitForSelector('#activity-list .act-item', { timeout: 15_000 }).catch(() => null);

        const matched = errors.filter(e => /notifIcon|notifClass|ReferenceError/.test(e));
        expect(matched, 'No notifIcon/notifClass ReferenceErrors should be thrown').toEqual([]);
    });
});
