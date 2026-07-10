# Collapsible Side Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the right-side Services/VPN/Host panel collapsible to a thin edge strip on the right of the viewport. On mobile the panel defaults to collapsed; on desktop it defaults to open but can be collapsed. When any service is down or VPN is degraded, the collapsed strip glows yellow to alert the user without taking over the screen.

**Architecture:** Pure frontend change in the static dashboard (`nginx/*`). A single `updatePanelAlert()` function derives a `body.panel-alert` class from the existing service-health and VPN-health data already refreshed every 30 s by `checkServices()` and `checkVPN()` — no new endpoints, no new polling. Collapse state is a `body.side-collapsed` class persisted to `localStorage` under `pelicula_side_collapsed`, with mobile default applied only when no preference is stored. A thin `<button id="side-strip">` lives as a sibling of `.pane-side` inside `.main`; it is visible only when the body has `side-collapsed` and toggles the class on click. On mobile (`max-width: 768px`) the grid collapses to single-column and `.pane-side` becomes a fixed-position overlay that slides in when open.

**Tech Stack:** Vanilla HTML/CSS/JS (no frameworks), Playwright for the spec (existing infrastructure in `tests/playwright/`).

---

## File Structure

```
nginx/
  index.html          # Add <button id="side-strip"> sibling of .pane-side inside .main;
                      # add collapse button inside the Services sidebar-hdr.
  dashboard.js        # Add updatePanelAlert(), side-panel state machine,
                      # click/init wiring. Hook into updateSvcTotals() +
                      # updateVPNPortBanner().
  styles.css          # Add .side-strip rules, body.side-collapsed / body.panel-alert
                      # modifiers, and the @media (max-width: 768px) mobile layout.
tests/playwright/
  specs/
    side-panel.spec.js  # NEW — Playwright spec covering default state, strip click,
                        # click-outside, desktop collapse, alert glow on down service.
```

**Decomposition rationale:** The feature is tightly coupled across the three dashboard files (a DOM element in `index.html`, styles in `styles.css`, behavior in `dashboard.js`). Splitting by file would produce half-working commits. Instead, each task produces a vertically-sliced, independently-testable behavior (alert derivation → collapse machinery → alert glow). The spec is written test-first in Task 1 so later tasks have a red-green loop.

---

## Pre-Task: Branch Setup

- [ ] **Step 1: Confirm a clean tree**

```bash
cd /Users/gwen/workspace/pelicula
git status
```

Expected: `nothing to commit, working tree clean`. If dirty, stop and ask the user before continuing.

- [ ] **Step 2: Create the feature branch**

```bash
git checkout -b feature/collapsible-side-panel
```

---

## Task 1: Panel-alert health signal (JS)

**Goal:** Add `updatePanelAlert()` that reads existing per-service `.svc-pip.down` state plus an internal VPN-degraded flag, and toggles `body.panel-alert`. Hook it into the two functions that already fire whenever that data changes: `updateSvcTotals()` (service refresh) and `updateVPNPortBanner()` (VPN refresh). No UI surface yet — the class is observable via DOM only.

**Files:**
- Create: `tests/playwright/specs/side-panel.spec.js`
- Modify: `nginx/dashboard.js` (edit `updateSvcTotals` at ~line 545, `updateVPNPortBanner` at ~line 739; add new helpers)

- [ ] **Step 1: Write the failing Playwright spec (just the alert-glow test)**

Create `tests/playwright/specs/side-panel.spec.js` with this content:

```js
// tests/playwright/specs/side-panel.spec.js
const { test, expect } = require('@playwright/test');
const { ensureLoggedIn } = require('../helpers/api');

const MOBILE_VIEWPORT = { width: 400, height: 800 };
const DESKTOP_VIEWPORT = { width: 1280, height: 900 };

const ALL_SERVICES = ['prowlarr', 'sonarr', 'radarr', 'qbittorrent', 'procula', 'jellyfin', 'bazarr'];

// Mock /api/pelicula/status so we can deterministically put a service "down".
// Pass { down: ['sonarr'] } to simulate a single down service.
function mockStatus(page, { down = [] } = {}) {
    return page.route('**/api/pelicula/status', async (route) => {
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
}

// Playwright gives each test a fresh BrowserContext by default, so
// localStorage is clean at the start of every test — no explicit clearing
// needed. This lets `page.reload()` keep the collapse preference within a
// single test, which the "preference persists" case in Task 2 relies on.

test.describe('Collapsible side panel', () => {
    test('body gains panel-alert class when a service is down', async ({ page }) => {
        await mockStatus(page, { down: ['sonarr'] });
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);
        // checkServices runs on load; poll for the class.
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
});
```

- [ ] **Step 2: Run the spec to verify it fails**

Make sure the test stack is running (if not: `./tests/e2e.sh up` in another terminal and wait for it to be healthy on port 7399). Then:

```bash
cd /Users/gwen/workspace/pelicula
npx playwright test --config tests/playwright/playwright.config.js tests/playwright/specs/side-panel.spec.js
```

Expected: Both tests FAIL because `body` never gains `panel-alert`. If the tests fail for a different reason (e.g., `ensureLoggedIn` timeout), resolve that before continuing — it indicates the stack isn't ready.

- [ ] **Step 3: Add `updatePanelAlert()` and the VPN-degraded flag in dashboard.js**

Open `nginx/dashboard.js`. Find the `// ── Services ──────────────────────────────` comment block (around line 455) and insert the following *immediately above* it:

```js
// ── Side panel health signal ──────────────
// Derives body.panel-alert from service down-count + VPN degraded flag.
// Called by updateSvcTotals() and updateVPNPortBanner() — no new polling.
let _panelVPNDegraded = false;

function updatePanelAlert() {
    const pips = document.querySelectorAll('#svc-sidebar-list .svc-pip');
    let down = 0;
    pips.forEach(p => { if (p.classList.contains('down')) down++; });
    const unhealthy = down > 0 || _panelVPNDegraded;
    document.body.classList.toggle('panel-alert', unhealthy);
}
```

- [ ] **Step 4: Hook `updatePanelAlert()` into `updateSvcTotals()`**

In `nginx/dashboard.js`, find the `updateSvcTotals()` function (around line 545). At the very end of the function, after the existing `if/else if/else` block that sets `el.textContent`, add a call:

Change this:

```js
    } else {
        el.textContent = '';
        el.style.color = '';
    }
}
```

to this:

```js
    } else {
        el.textContent = '';
        el.style.color = '';
    }
    updatePanelAlert();
}
```

- [ ] **Step 5: Hook `updatePanelAlert()` into `updateVPNPortBanner()`**

In `nginx/dashboard.js`, find `function updateVPNPortBanner(degraded)` (around line 739). At the very top of the function body, before the `const bannerId = ...` line, set the flag and trigger an update:

Change this:

```js
function updateVPNPortBanner(degraded) {
    const bannerId = 'vpn-port-warn-banner';
```

to this:

```js
function updateVPNPortBanner(degraded) {
    _panelVPNDegraded = degraded;
    updatePanelAlert();
    const bannerId = 'vpn-port-warn-banner';
```

- [ ] **Step 6: Also clear the flag in the `checkVPN()` catch path**

The existing `checkVPN()` catch block (around line 731) does not call `updateVPNPortBanner()`, so a thrown error won't currently unset `_panelVPNDegraded`. We don't need to touch that — a VPN telemetry error is itself a reason the user might want the alert lit. Leave `_panelVPNDegraded` with its last value on error. Skip; no edit for this step beyond reading and confirming the decision.

- [ ] **Step 7: Run the spec to verify both tests pass**

```bash
cd /Users/gwen/workspace/pelicula
npx playwright test --config tests/playwright/playwright.config.js tests/playwright/specs/side-panel.spec.js
```

Expected: Both tests PASS.

- [ ] **Step 8: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add nginx/dashboard.js tests/playwright/specs/side-panel.spec.js
git commit -m "$(cat <<'EOF'
feat(dashboard): derive body.panel-alert from service + VPN health

Adds updatePanelAlert() and hooks it into updateSvcTotals() and
updateVPNPortBanner(). No user-visible change yet — this is the
signal the collapsible side strip will render in the next commit.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Collapse machinery + edge strip

**Goal:** Add the state machine (`setSidePanelCollapsed`, `toggleSidePanel`, `initSidePanelState`), the `<button id="side-strip">` DOM element, the responsive CSS that hides `.pane-side` when collapsed and floats it over main content on mobile, and the click-outside-to-close behavior. Mobile default is collapsed; desktop default is open; both respect stored preference.

**Files:**
- Modify: `tests/playwright/specs/side-panel.spec.js` (add cases)
- Modify: `nginx/index.html` (add `<button id="side-strip">` and desktop collapse button)
- Modify: `nginx/dashboard.js` (add state machine + wiring)
- Modify: `nginx/styles.css` (add responsive rules)

- [ ] **Step 1: Add the collapse-behavior tests to the spec**

Edit `tests/playwright/specs/side-panel.spec.js` and add these tests inside the existing `test.describe('Collapsible side panel', ...)` block, after the two tests already there:

```js
    test('mobile: panel is collapsed by default and strip is visible', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await expect(page.locator('#side-strip')).toBeVisible();
        await expect(page.locator('.pane-side')).toBeHidden();
    });

    test('mobile: tapping the strip opens the panel', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        await page.locator('#side-strip').click();
        await expect(page.locator('body')).not.toHaveClass(/side-collapsed/);
        await expect(page.locator('.pane-side')).toBeVisible();
    });

    test('mobile: tapping outside the open panel collapses it', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        await page.locator('#side-strip').click();
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
        await page.locator('#side-collapse-btn').click();
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await expect(page.locator('#side-strip')).toBeVisible();
    });

    test('preference persists across reloads', async ({ page }) => {
        await mockStatus(page, {});
        await page.setViewportSize(DESKTOP_VIEWPORT);
        await ensureLoggedIn(page);
        await page.locator('#side-collapse-btn').click();
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
        await page.reload();
        await expect(page.locator('body')).toHaveClass(/side-collapsed/);
    });
```

- [ ] **Step 2: Run the new tests to verify they fail**

```bash
cd /Users/gwen/workspace/pelicula
npx playwright test --config tests/playwright/playwright.config.js tests/playwright/specs/side-panel.spec.js
```

Expected: the five new tests FAIL (element `#side-strip` / `#side-collapse-btn` not found, body class not set). The two alert tests from Task 1 still PASS.

- [ ] **Step 3: Add the edge-strip element to `index.html`**

Open `nginx/index.html`. Find the `<!-- ── Right sidebar ── -->` block (around line 693). Immediately *before* the `<div class="pane-side">` line, insert the strip button:

Change this:

```html
        <!-- ── Right sidebar ──────────────────────────────────── -->
        <div class="pane-side">
```

to this:

```html
        <!-- ── Right sidebar ──────────────────────────────────── -->
        <button type="button" class="side-strip" id="side-strip" aria-label="Open services panel"></button>
        <div class="pane-side">
```

- [ ] **Step 4: Add the desktop collapse button inside the Services header**

In `nginx/index.html`, find the `<div class="svc-header-actions">` block (around line 699). Immediately after the line `<span class="svc-refresh-status" id="svc-refresh-status"></span>`, add the collapse button:

Change this:

```html
                <div class="svc-header-actions">
                    <span class="svc-refresh-status" id="svc-refresh-status"></span>
                    <button class="section-action" id="svc-refresh-btn" title="Refresh now" onclick="manualRefreshServices()">&#8635;</button>
```

to this:

```html
                <div class="svc-header-actions">
                    <span class="svc-refresh-status" id="svc-refresh-status"></span>
                    <button class="section-action" id="side-collapse-btn" title="Collapse sidebar" onclick="setSidePanelCollapsed(true)">&times;</button>
                    <button class="section-action" id="svc-refresh-btn" title="Refresh now" onclick="manualRefreshServices()">&#8635;</button>
```

- [ ] **Step 5: Add the state-machine functions in `dashboard.js`**

Open `nginx/dashboard.js`. Find the block you added in Task 1 (the `// ── Side panel health signal ──` comment with `updatePanelAlert`). *Immediately after* the closing brace of `updatePanelAlert()`, add the state machine:

```js

// ── Side panel collapse state ─────────────
// Collapse state is persisted under this localStorage key. When no preference
// is stored, mobile viewports default to collapsed and desktops default to open.
const _SIDE_COLLAPSED_KEY = 'pelicula_side_collapsed';
const _SIDE_MOBILE_MAX = 768;

function _isMobileViewport() {
    return window.innerWidth <= _SIDE_MOBILE_MAX;
}

function setSidePanelCollapsed(collapsed) {
    document.body.classList.toggle('side-collapsed', !!collapsed);
    try { localStorage.setItem(_SIDE_COLLAPSED_KEY, collapsed ? '1' : '0'); } catch (e) {}
}

function toggleSidePanel() {
    setSidePanelCollapsed(!document.body.classList.contains('side-collapsed'));
}

function initSidePanelState() {
    let stored = null;
    try { stored = localStorage.getItem(_SIDE_COLLAPSED_KEY); } catch (e) {}
    if (stored === '1') { setSidePanelCollapsed(true); return; }
    if (stored === '0') { setSidePanelCollapsed(false); return; }
    // No preference — default based on viewport.
    setSidePanelCollapsed(_isMobileViewport());
}

// Strip click opens the panel.
document.addEventListener('DOMContentLoaded', () => {
    initSidePanelState();
    const strip = document.getElementById('side-strip');
    if (strip) {
        strip.addEventListener('click', (e) => {
            e.stopPropagation();
            setSidePanelCollapsed(false);
        });
    }
});

// Click-outside-to-close: only on mobile, only when panel is currently open.
document.addEventListener('click', (e) => {
    if (!_isMobileViewport()) return;
    if (document.body.classList.contains('side-collapsed')) return;
    const paneSide = document.querySelector('.pane-side');
    if (!paneSide || paneSide.contains(e.target)) return;
    const strip = document.getElementById('side-strip');
    if (strip && strip.contains(e.target)) return;
    setSidePanelCollapsed(true);
});
```

- [ ] **Step 6: Add responsive CSS to `styles.css`**

Open `nginx/styles.css`. Find the `.pane-side` rule (around line 246). Leave it as-is. Scroll to the end of the file. Append:

```css

/* ── Collapsible side panel ────────────────────────────────────────────── */

/* Edge strip — a thin fixed-position button that lives on the right edge
   of the viewport. Visible only when the panel is collapsed. */
.side-strip {
    display: none;
    position: fixed;
    top: 94px;       /* match header height */
    bottom: 54px;    /* match footer height */
    right: 0;
    width: 8px;
    padding: 0;
    border: none;
    background: var(--border);
    cursor: pointer;
    z-index: 50;
    transition: background 0.2s, width 0.15s;
}
.side-strip:hover { width: 12px; background: var(--border2); }

body.side-collapsed .side-strip { display: block; }
body.side-collapsed .pane-side { display: none; }
body.side-collapsed .main { grid-template-columns: 1fr; }

/* Desktop collapse button — a small × inside the Services header. Hidden
   on mobile (the strip handles opening and click-outside handles closing). */
#side-collapse-btn { display: inline-flex; }

/* Mobile layout: single column, pane-side floats over main content when open. */
@media (max-width: 768px) {
    .main { grid-template-columns: 1fr; }
    .pane-side {
        position: fixed;
        top: 94px;
        bottom: 54px;
        right: 0;
        width: min(300px, 85vw);
        z-index: 100;
        overflow-y: auto;
        box-shadow: -4px 0 24px rgba(0, 0, 0, 0.4);
    }
    #side-collapse-btn { display: none; }
}
```

- [ ] **Step 7: Restart the dashboard container so nginx picks up the static file changes**

```bash
cd /Users/gwen/workspace/pelicula
./pelicula restart nginx
```

(Or if running against the test stack on port 7399, restart that stack's nginx container. Static-file edits in the host `nginx/` directory are bind-mounted in, but nginx caches some responses — a restart guarantees a clean slate.)

- [ ] **Step 8: Run the full spec file and verify all seven tests pass**

```bash
cd /Users/gwen/workspace/pelicula
npx playwright test --config tests/playwright/playwright.config.js tests/playwright/specs/side-panel.spec.js
```

Expected: all 7 tests PASS.

- [ ] **Step 9: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add nginx/index.html nginx/dashboard.js nginx/styles.css tests/playwright/specs/side-panel.spec.js
git commit -m "$(cat <<'EOF'
feat(dashboard): collapsible side panel with edge strip

Adds a thin right-edge strip (#side-strip) that reveals the Services/VPN
panel on click. Mobile (<=768px) defaults to collapsed; desktop defaults
to open with a new × button in the Services header to collapse. State
persists to localStorage. On mobile the open panel floats over the main
content and click-outside collapses it.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Alert glow on the strip

**Goal:** When `body.panel-alert` is set (from Task 1's derivation), the edge strip glows yellow so the user can see the alarm without expanding. Single color — no yellow/red distinction, per the vision doc.

**Files:**
- Modify: `tests/playwright/specs/side-panel.spec.js` (add glow-verification test)
- Modify: `nginx/styles.css` (add glow rule)

- [ ] **Step 1: Add the glow-verification test to the spec**

Edit `tests/playwright/specs/side-panel.spec.js` and add this test inside the same `test.describe` block:

```js
    test('strip glows yellow when a service is down', async ({ page }) => {
        await mockStatus(page, { down: ['sonarr'] });
        await page.setViewportSize(MOBILE_VIEWPORT);
        await ensureLoggedIn(page);
        // Panel should be collapsed by default on mobile, so strip is visible.
        await expect(page.locator('body')).toHaveClass(/panel-alert/);
        const strip = page.locator('#side-strip');
        await expect(strip).toBeVisible();
        // Verify strip background is the lemon accent (not the default border color).
        const bg = await strip.evaluate(el => getComputedStyle(el).backgroundColor);
        // Lemon = #f8d040 = rgb(248, 208, 64). Assert the red channel is high
        // and the blue channel is low — distinguishes yellow from the grey default.
        const match = bg.match(/rgb\((\d+),\s*(\d+),\s*(\d+)/);
        expect(match).not.toBeNull();
        const [, r, g, b] = match.map(Number);
        expect(r).toBeGreaterThan(200);
        expect(b).toBeLessThan(150);
    });
```

- [ ] **Step 2: Run the spec and verify this test fails**

```bash
cd /Users/gwen/workspace/pelicula
npx playwright test --config tests/playwright/playwright.config.js tests/playwright/specs/side-panel.spec.js -g "glows yellow"
```

Expected: the glow test FAILS (the strip background is still the default `var(--border)` grey).

- [ ] **Step 3: Add the glow CSS**

Open `nginx/styles.css`. Find the `body.side-collapsed .main { grid-template-columns: 1fr; }` line you added in Task 2. Immediately after that line (and before the `#side-collapse-btn { display: inline-flex; }` rule), insert:

```css

/* Alert glow — lights up the strip when something in pane-side is unhealthy. */
body.panel-alert .side-strip {
    background: #f8d040;
    box-shadow: 0 0 12px rgba(248, 208, 64, 0.7), -2px 0 18px rgba(248, 208, 64, 0.5);
    animation: side-strip-pulse 2s ease-in-out infinite;
}
body.panel-alert .side-strip:hover { background: #f8d040; }

@keyframes side-strip-pulse {
    0%, 100% { opacity: 1; }
    50%      { opacity: 0.65; }
}
```

- [ ] **Step 4: Restart nginx so CSS edits are picked up**

```bash
cd /Users/gwen/workspace/pelicula
./pelicula restart nginx
```

- [ ] **Step 5: Run the full spec file to verify all tests pass**

```bash
cd /Users/gwen/workspace/pelicula
npx playwright test --config tests/playwright/playwright.config.js tests/playwright/specs/side-panel.spec.js
```

Expected: all 8 tests PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add nginx/styles.css tests/playwright/specs/side-panel.spec.js
git commit -m "$(cat <<'EOF'
feat(dashboard): yellow glow on side strip when panel is unhealthy

Binds the body.panel-alert signal from the earlier commit to a pulsing
yellow glow on the collapsed edge strip, using the existing lemon accent
color. Mobile users now see an at-a-glance alarm without expanding the
panel.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Manual verification on a real phone

Automated specs cover the state transitions and the glow color; they cannot verify how the panel actually feels on a small screen. Do these checks by hand before merging.

- [ ] **Step 1: Load the dashboard on a real mobile browser**

On your phone, open `http://192.168.1.117:7354/` (or whatever the LAN address of the stack is). Confirm:

- The edge strip is visible on the right.
- The Activity feed and search CTA are readable and fill the width — no obscuring overlay.
- Tapping the strip opens the panel smoothly and it occupies `min(300px, 85vw)` on the right.
- Tapping anywhere in the main content (outside the panel) closes it.
- Tapping the Services/VPN items inside the open panel still navigates normally (the click-outside handler should not fire).

- [ ] **Step 2: Force an unhealthy state and verify the glow**

On the desktop dashboard, open the Services header menu (kebab → Restart stack) or stop a single service from the CLI:

```bash
cd /Users/gwen/workspace/pelicula
docker compose -f compose/docker-compose.yml stop sonarr
```

Then refresh the phone browser and confirm within ~30 s (one `checkServices` cycle):

- The edge strip turns yellow and pulses.
- `document.body.classList` contains `panel-alert` (inspect via remote debugger if needed).
- Restart the service: `docker compose -f compose/docker-compose.yml start sonarr`. After the next refresh cycle, the glow goes away.

- [ ] **Step 3: Verify desktop still works**

On a desktop browser at 1280×900:

- Panel is open by default, layout is identical to before this change.
- The new × button in the Services header is visible. Clicking it collapses the panel, the strip appears, `localStorage.pelicula_side_collapsed === '1'`.
- Refreshing the page keeps it collapsed. Clicking the strip opens it again.
- Clearing `localStorage.pelicula_side_collapsed` and refreshing returns to the open default.

- [ ] **Step 4: Final spec run + status check**

```bash
cd /Users/gwen/workspace/pelicula
npx playwright test --config tests/playwright/playwright.config.js tests/playwright/specs/side-panel.spec.js
git status
git log --oneline feature/collapsible-side-panel ^main
```

Expected: all 8 tests PASS. Working tree clean. Three commits on the branch (one per task).

- [ ] **Step 5: Hand off to merge decision**

Stop here. The branch is ready. Ask the user whether to fast-forward merge into `main` locally (per their workflow preference) or open a PR. Do not merge without explicit instruction.

---

## Open Questions (from the vision doc)

These were deferred during /noodle and do not block this plan:

- **Yellow vs. red distinction**: currently a single yellow glow. If the user later wants degraded-vs-broken split, add a second class (e.g., `body.panel-alert-critical`) with a red glow, derived from a stricter condition (e.g., Radarr *and* Sonarr both down, or VPN completely unreachable).
- **Swipe gesture to open the panel**: currently tap-only. Can be added later with a `touchstart`/`touchmove` handler on the strip.
- **Panel slide-in animation**: currently the panel appears instantly. A CSS `transform: translateX()` transition on `.pane-side` would be a small polish.
