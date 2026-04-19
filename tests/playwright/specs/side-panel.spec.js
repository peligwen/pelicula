// tests/playwright/specs/side-panel.spec.js
const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

const MOBILE_VIEWPORT = { width: 400, height: 800 };
const DESKTOP_VIEWPORT = { width: 1280, height: 900 };

const ALL_SERVICES = ['prowlarr', 'sonarr', 'radarr', 'qbittorrent', 'procula', 'jellyfin', 'bazarr'];

// Mock both /api/pelicula/status (service pips) and /api/pelicula/health
// (VPN watchdog) so the panel-alert signal is isolated from real backend
// state. ALL_SERVICES must stay in sync with the #svc-pip-* elements
// defined in nginx/index.html (~line 712).
async function mockStatus(page, { down = [] } = {}) {
    await page.route('**/api/pelicula/status', async (route) => {
        const services = {};
        for (const name of ALL_SERVICES) {
            services[name] = down.includes(name) ? 'down' : 'up';
        }
        await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ services }),
        });
    });
    await page.route('**/api/pelicula/health', async (route) => {
        await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ vpn: { port_status: 'ok' } }),
        });
    });
}

// NOTE (for Task 2): Playwright gives each test a fresh BrowserContext by
// default, so localStorage is clean at the start of every test — no
// explicit clearing needed. This lets `page.reload()` keep the collapse
// preference within a single test, which the "preference persists" case
// in Task 2 relies on.

test.describe('Collapsible side panel', () => {
    test('body gains panel-alert class when a service is down', async ({ page }) => {
        await mockStatus(page, { down: ['sonarr'] });
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);
        // Prove the mock took effect before asserting the derived class.
        await expect(page.locator('#svc-pip-sonarr')).toHaveClass(/down/);
        await expect(page.locator('body')).toHaveClass(/panel-alert/);
    });

    test('body does not have panel-alert class when all services are up', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);
        // Wait for at least one pip to turn green, proving checkServices ran.
        await expect(page.locator('#svc-pip-sonarr')).toHaveClass(/up/);
        await expect(page.locator('body')).not.toHaveClass(/panel-alert/);
    });

    test('mobile: panel is collapsed by default and strip is visible', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await expect(page.locator('#side-toggle')).toBeVisible();
        await expect(page.locator('.pane-side')).toBeHidden();
    });

    test('mobile: tapping the strip opens the panel', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        await page.locator('#side-toggle').click();
        await expect(page.locator('body')).not.toHaveClass(/side-collapsed/);
        await expect(page.locator('.pane-side')).toBeVisible();
    });

    test('mobile: tapping outside the open panel collapses it', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        await page.locator('#side-toggle').click();
        await expect(page.locator('.pane-side')).toBeVisible();
        // Click somewhere inside pane-main, well away from the panel.
        await page.locator('.pane-main').click({ position: { x: 20, y: 200 } });
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
    });

    test('desktop: panel is open by default; collapse button hides it', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);
        await expect(page.locator('body')).not.toHaveClass(/side-collapsed/);
        await expect(page.locator('.pane-side')).toBeVisible();
        await page.locator('#side-toggle').click();
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await expect(page.locator('#side-toggle')).toBeVisible();
    });

    test('mobile: clicks inside an open modal do not collapse the panel', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        // Open the panel so the tap-outside handler is armed.
        await page.locator('#side-toggle').click();
        await expect(page.locator('.pane-side')).toBeVisible();
        // Force a visible modal overlay (none of the built-in modals are
        // reachable from the mobile dashboard yet, so inject a stub).
        await page.evaluate(() => {
            const overlay = document.createElement('div');
            overlay.className = 'modal-overlay';
            overlay.id = 'test-modal-overlay';
            overlay.style.cssText = 'position:fixed;inset:0;z-index:500;background:rgba(0,0,0,0.5);';
            const btn = document.createElement('button');
            btn.id = 'test-modal-btn';
            btn.textContent = 'click me';
            btn.style.cssText = 'position:absolute;left:20px;top:200px;';
            overlay.appendChild(btn);
            document.body.appendChild(overlay);
        });
        await page.locator('#test-modal-btn').click();
        // Panel must still be open because a modal is visible.
        await expect(page.locator('body')).not.toHaveClass(/side-collapsed/);
    });

    test('preference persists across reloads', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);
        await page.locator('#side-toggle').click();
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await page.reload();
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
    });

    test('alert glow: collapsed strip animates when a service is down', async ({ page }) => {
        await mockStatus(page, { down: ['sonarr'] });
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        // Mobile defaults to collapsed; wait until the alert class has propagated.
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await expect(page.locator('body')).toHaveClass(/panel-alert/);
        // The strip should have a non-'none' animation-name when in alert state.
        const animName = await page.locator('#side-toggle').evaluate(
            (el) => getComputedStyle(el).animationName
        );
        expect(animName).not.toBe('none');
        expect(animName.length).toBeGreaterThan(0);
    });

    test('alert glow: collapsed strip has no animation when all services are up', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await expect(page.locator('body')).not.toHaveClass(/panel-alert/);
        const animName = await page.locator('#side-toggle').evaluate(
            (el) => getComputedStyle(el).animationName
        );
        expect(animName).toBe('none');
    });

    test('alert glow: respects prefers-reduced-motion', async ({ browser }) => {
        const context = await browser.newContext({ reducedMotion: 'reduce' });
        const page = await context.newPage();
        try {
            await mockStatus(page, { down: ['sonarr'] });
            await page.setViewportSize(MOBILE_VIEWPORT);
            await ensureLoggedIn(page);
            await expect(page.locator('body')).toHaveClass(/side-collapsed/);
            await expect(page.locator('body')).toHaveClass(/panel-alert/);
            // Animation is suppressed under reduced-motion; the strip should
            // still be visually distinct (static yellow background).
            const animName = await page.locator('#side-toggle').evaluate(
                (el) => getComputedStyle(el).animationName
            );
            expect(animName).toBe('none');
            const bg = await page.locator('#side-toggle').evaluate(
                (el) => getComputedStyle(el).backgroundColor
            );
            // #ffcb3d
            expect(bg).toBe('rgb(255, 203, 61)');
        } finally {
            await context.close();
        }
    });
});
