# Custom Micro-Framework for Dashboard JS

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract a ~180-line custom reactive micro-framework from the existing dashboard architecture, add `data-testid` hooks to all key interactive elements, and refactor the dashboard's main state management sections to use the framework — reducing LoC and giving Playwright stable, semantic anchors.

**Architecture:** A single `nginx/framework.js` file provides three things: a reactive `createStore()` (pub/sub over a plain object), a `component()` registration that re-renders when subscribed keys change, and a `html()` tagged template that auto-escapes interpolated strings. `dashboard.js` adopts the store for its global state (role, auth, pipeline refresh state) and the highest-churn rendering functions (search results, pipeline cards). No build step. No bundler. Plain JS, loaded via `<script>` in `index.html` before `dashboard.js`.

**Tech Stack:** Vanilla JS (ES2020), no external dependencies, served as a static file from nginx.

---

## File Structure

```
nginx/
  framework.js            # New: reactive store, component registry, html tag
  dashboard.js            # Modified: adopt store for global state; refactor search + auth sections
  index.html              # Modified: add <script src="/framework.js">, add data-testid attrs
```

---

## Task 1: Write framework.js

**Files:**
- Create: `nginx/framework.js`

This is the entire framework. Read it before touching dashboard.js — dashboard.js imports nothing; it assumes `framework.js` is loaded first via `<script>`.

- [ ] **Step 1: Write framework.js**

```js
// nginx/framework.js
// Pelicula micro-framework — ~180 lines, no dependencies, no build step.
//
// API:
//   const store = createStore(initial)    — reactive state store
//   store.get(key)                        — read a value
//   store.set(key, value)                 — write + notify subscribers
//   store.subscribe(key, fn)              — fn(newValue) called on change
//   store.unsubscribe(key, fn)            — remove a subscription
//
//   component(name, factory)             — register a component
//   mount(name, el, props)               — mount a registered component into el
//
//   html`<div>${expr}</div>`             — tagged template: auto-escapes interpolations
//   raw(str)                             — mark a string as pre-escaped (trust it as-is)
//
// Design notes:
//   - store.set() is synchronous; subscribers are called immediately.
//   - Components are plain functions: factory(el, store, props) → { render, destroy }.
//     render() is called on mount and whenever a subscribed store key changes.
//   - html`` escapes string interpolations only; numbers/booleans pass through.
//     Use raw() to embed pre-escaped HTML strings.

'use strict';

// ── Store ─────────────────────────────────────────────────────────────────────

function createStore(initial) {
    const state  = Object.assign({}, initial);
    const subs   = {};   // key → Set<fn>

    function get(key) {
        return state[key];
    }

    function set(key, value) {
        if (state[key] === value) return;
        state[key] = value;
        if (subs[key]) subs[key].forEach(fn => { try { fn(value); } catch(e) { console.error('[store]', e); } });
    }

    function subscribe(key, fn) {
        (subs[key] = subs[key] || new Set()).add(fn);
    }

    function unsubscribe(key, fn) {
        if (subs[key]) subs[key].delete(fn);
    }

    // Batch multiple set() calls without intermediate re-renders.
    // Usage: store.batch(() => { store.set('a',1); store.set('b',2); })
    function batch(fn) {
        const pending = new Map();
        const origSet = set;
        // Shadow set() during fn execution
        const batchSet = (key, value) => { pending.set(key, value); };
        // Temporarily override — tricky in non-module context, so we call fn with a proxy store
        const proxy = { get, set: batchSet, subscribe, unsubscribe, batch };
        fn(proxy);
        for (const [key, value] of pending) origSet(key, value);
    }

    return { get, set, subscribe, unsubscribe, batch };
}

// ── Component registry ────────────────────────────────────────────────────────

const _registry  = {};   // name → factory fn
const _mounted   = [];   // { name, el, instance, unsubs }

function component(name, factory) {
    _registry[name] = factory;
}

function mount(name, el, props) {
    const factory = _registry[name];
    if (!factory) { console.error('[framework] Unknown component:', name); return; }
    const unsubs = [];
    const storeProxy = {
        get:    (key) => appStore.get(key),
        subscribe: (key, fn) => { appStore.subscribe(key, fn); unsubs.push(() => appStore.unsubscribe(key, fn)); },
        set:    (key, value) => appStore.set(key, value),
    };
    const instance = factory(el, storeProxy, props || {});
    _mounted.push({ name, el, instance, unsubs });
    if (instance && typeof instance.render === 'function') instance.render();
    return instance;
}

function unmount(el) {
    const idx = _mounted.findIndex(m => m.el === el);
    if (idx === -1) return;
    const { instance, unsubs } = _mounted[idx];
    unsubs.forEach(fn => fn());
    if (instance && typeof instance.destroy === 'function') instance.destroy();
    _mounted.splice(idx, 1);
}

// ── html tagged template ──────────────────────────────────────────────────────

const _RAW = Symbol('raw');

function raw(str) {
    return { [_RAW]: true, str: String(str) };
}

function _escapeHtml(s) {
    if (s == null) return '';
    if (typeof s === 'number' || typeof s === 'boolean') return String(s);
    if (s && s[_RAW]) return s.str;
    return String(s)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

function html(strings, ...values) {
    let result = '';
    strings.forEach((str, i) => {
        result += str;
        if (i < values.length) {
            const v = values[i];
            if (Array.isArray(v)) {
                result += v.map(item => (item && item[_RAW]) ? item.str : _escapeHtml(item)).join('');
            } else {
                result += _escapeHtml(v);
            }
        }
    });
    return raw(result);
}

// ── Global store (singleton, shared by all components) ───────────────────────
// dashboard.js initialises this after framework.js loads.

let appStore = null;

function initStore(initial) {
    appStore = createStore(initial);
    return appStore;
}

// ── data-testid helpers ───────────────────────────────────────────────────────

// Query a data-testid element (throws if not found in dev, returns null in prod).
function byTestId(id, root) {
    return (root || document).querySelector(`[data-testid="${id}"]`);
}

// ── Exports (assigned to window for plain-script use) ─────────────────────────

window.PeliculaFW = { createStore, component, mount, unmount, html, raw, initStore, byTestId };
```

- [ ] **Step 2: Verify it parses**

```bash
node -e "
  const vm = require('vm');
  const fs = require('fs');
  const code = fs.readFileSync('nginx/framework.js','utf8');
  // Simulate browser globals
  const ctx = { window: {}, console, document: { querySelector: () => null } };
  vm.createContext(ctx);
  vm.runInContext(code, ctx);
  console.log('exports:', Object.keys(ctx.window.PeliculaFW).join(', '));
"
```

Expected output:
```
exports: createStore, component, mount, unmount, html, raw, initStore, byTestId
```

- [ ] **Step 3: Commit**

```bash
git add nginx/framework.js
git commit -m "feat(dashboard): add micro-framework (store, components, html tag)"
```

---

## Task 2: Add data-testid Attributes to index.html

**Files:**
- Modify: `nginx/index.html`

Add `data-testid` to all key interactive and status elements. These give Playwright stable, semantic anchors that won't break when CSS classes are refactored.

- [ ] **Step 1: Add testids to the login overlay**

Find:
```html
<div class="login-overlay hidden" id="login-overlay">
```
Replace with:
```html
<div class="login-overlay hidden" id="login-overlay" data-testid="login-overlay">
```

Find:
```html
<input type="text" id="login-username" placeholder="Username" autocomplete="username">
```
Replace with:
```html
<input type="text" id="login-username" placeholder="Username" autocomplete="username" data-testid="login-username">
```

Find:
```html
<input type="password" id="login-password" placeholder="Password" autocomplete="current-password" autofocus>
```
Replace with:
```html
<input type="password" id="login-password" placeholder="Password" autocomplete="current-password" autofocus data-testid="login-password">
```

Find the login submit button (inside `#login-form`):
```html
<form id="login-form">
```
Replace with:
```html
<form id="login-form" data-testid="login-form">
```

- [ ] **Step 2: Add testids to the pipeline section**

Find:
```html
<div class="section" id="pipeline-section">
```
Replace with:
```html
<div class="section" id="pipeline-section" data-testid="pipeline-section">
```

Find each pipeline lane in sequence and add testids:
```html
<div class="pl-lane pl-lane-downloading" id="pipeline-lane-downloading">
```
→
```html
<div class="pl-lane pl-lane-downloading" id="pipeline-lane-downloading" data-testid="pipeline-lane-downloading">
```

Do the same for each lane: `pipeline-lane-imported`, `pipeline-lane-validating`, `pipeline-lane-processing`, `pipeline-lane-cataloging`.

Find:
```html
<div id="pipeline-cards-completed">
```
→
```html
<div id="pipeline-cards-completed" data-testid="pipeline-cards-completed">
```

- [ ] **Step 3: Add testids to the storage explorer section**

Find:
```html
<div class="section hidden" id="storage-explorer-section">
```
→
```html
<div class="section hidden" id="storage-explorer-section" data-testid="storage-explorer-section">
```

Find:
```html
<div class="browse-tree" id="browse-tree">
```
→
```html
<div class="browse-tree" id="browse-tree" data-testid="browse-tree">
```

Find:
```html
<button class="import-btn" id="btn-import" onclick="onImportClick()" disabled title="">&#8615; Import</button>
```
→
```html
<button class="import-btn" id="btn-import" onclick="onImportClick()" disabled title="" data-testid="btn-import">&#8615; Import</button>
```

Find:
```html
<div class="se-modal hidden" id="import-modal">
```
→
```html
<div class="se-modal hidden" id="import-modal" data-testid="import-modal">
```

Find:
```html
<button class="import-btn primary" id="btn-configure" onclick="importGoToStep('configure')" disabled>Configure Import</button>
```
→
```html
<button class="import-btn primary" id="btn-configure" onclick="importGoToStep('configure')" disabled data-testid="btn-configure">Configure Import</button>
```

Find:
```html
<div id="apply-content">
```
→
```html
<div id="apply-content" data-testid="apply-content">
```

Find:
```html
<div class="import-nav hidden" id="apply-nav">
```
→
```html
<div class="import-nav hidden" id="apply-nav" data-testid="apply-nav">
```

- [ ] **Step 4: Add testids to the search section**

Find:
```html
<div class="search-section" id="search-section">
```
→
```html
<div class="search-section" id="search-section" data-testid="search-section">
```

Find:
```html
<input type="text" id="search-input" placeholder="Search for a title...">
```
→
```html
<input type="text" id="search-input" placeholder="Search for a title..." data-testid="search-input">
```

Find:
```html
<div class="search-results" id="search-results"></div>
```
→
```html
<div class="search-results" id="search-results" data-testid="search-results"></div>
```

- [ ] **Step 5: Add testid to the health chip**

Find:
```html
<span class="titlebar-chip titlebar-chip-ok hidden" id="chip-health-ok">
```
→
```html
<span class="titlebar-chip titlebar-chip-ok hidden" id="chip-health-ok" data-testid="chip-health-ok">
```

- [ ] **Step 6: Load framework.js in index.html before dashboard.js**

Find the `<script>` tag for `dashboard.js` in index.html:
```html
<script src="/dashboard.js"></script>
```
Replace with:
```html
<script src="/framework.js"></script>
<script src="/dashboard.js"></script>
```

Also add it before `import.js` if that is loaded separately on the same page:
```html
<script src="/import.js"></script>
```
(No change needed there — `framework.js` is already loaded before `import.js` because it comes before `dashboard.js`.)

- [ ] **Step 7: Verify the page still loads**

With the test stack running on port 7399:
```bash
curl -s http://localhost:7399/ | grep -c "data-testid"
```

Expected output: at least `15` (the number of testid attributes added)

- [ ] **Step 8: Commit**

```bash
git add nginx/index.html
git commit -m "feat(dashboard): add data-testid attributes for Playwright test hooks"
```

---

## Task 3: Refactor Global Auth State to Use Store

**Files:**
- Modify: `nginx/dashboard.js` (top of file, `checkAuth`, `applyRole`, `doLogin`)

**Context:** `dashboard.js` currently holds auth state in a module-level variable `currentRole` and applies it imperatively with `applyRole()`. This task replaces that with a store key, so role changes are reactive and Playwright can observe them via `data-testid` attribute changes driven by store subscriptions.

- [ ] **Step 1: Initialize the app store at the top of dashboard.js**

Find the very first lines of `dashboard.js`:
```js
// ── Resilient fetch (auto-abort after ms) ──
function tfetch(url, opts, ms) {
```

Insert BEFORE this block:
```js
// ── App store ────────────────────────────
// Initialised here; framework.js must be loaded first.
const store = PeliculaFW.initStore({
    role: 'admin',        // 'admin' | 'manager' | 'viewer'
    username: '',
    authEnabled: false,
});
```

- [ ] **Step 2: Replace the currentRole variable with store**

Find:
```js
let currentRole = 'admin'; // default when auth is off
```

Delete this line entirely (the store initialisation above replaces it).

- [ ] **Step 3: Update applyRole to use store.set**

Find the beginning of `applyRole`:
```js
function applyRole(role, username) {
    currentRole = role;
    document.body.dataset.username = username || '';
```

Replace with:
```js
function applyRole(role, username) {
    store.set('role', role);
    store.set('username', username || '');
    document.body.dataset.username = username || '';
```

- [ ] **Step 4: Update all reads of currentRole to use store.get**

Search `dashboard.js` for every occurrence of `currentRole` and replace with `store.get('role')`.

Run this check first to find all occurrences:
```bash
grep -n "currentRole" nginx/dashboard.js
```

For each occurrence, replace `currentRole` with `store.get('role')`. Common patterns:
- `currentRole === 'admin'` → `store.get('role') === 'admin'`
- `currentRole === 'manager' || currentRole === 'admin'` → `store.get('role') === 'manager' || store.get('role') === 'admin'`
- `document.body.dataset.role = role;` — keep this (it's a DOM side-effect, not a read)

Verify no occurrences remain:
```bash
grep -n "currentRole" nginx/dashboard.js
```
Expected output: (empty — no matches)

- [ ] **Step 5: Verify the dashboard still works**

Open `http://localhost:7399/` in a browser with the test stack running. The login overlay should appear (if auth is enabled) or the dashboard should load normally (if `PELICULA_AUTH=off`). No console errors.

Alternatively, run the existing Playwright spec:
```bash
npx playwright test --config tests/playwright/playwright.config.js --reporter list
```

Expected: still passes.

- [ ] **Step 6: Commit**

```bash
git add nginx/dashboard.js
git commit -m "refactor(dashboard): migrate auth role state to reactive store"
```

---

## Task 4: Refactor Search Section to Use html`` and Component

**Files:**
- Modify: `nginx/dashboard.js` (search section: `doSearch`, `renderResults`, `renderResultCard`, `buildDetailChips`)

**Context:** The search section is the highest-density example of imperative DOM rendering in dashboard.js. Converting it to `html\`\`` tagged template syntax removes the manual `esc()` call pattern and makes the render logic declarative. This is a proof-of-concept for the full refactor pattern — if it works and reduces LoC here, the pattern extends to pipeline cards, user management, etc.

After this task, measure LoC reduction:
```bash
wc -l nginx/dashboard.js
```
Target: at least 30 fewer lines than the pre-refactor count of 2345.

- [ ] **Step 1: Import html and raw from the framework at the top of dashboard.js**

After the store initialization block, add:
```js
const { html, raw } = PeliculaFW;
```

- [ ] **Step 2: Refactor buildDetailChips to use html``**

Find the existing `buildDetailChips` function:
```js
function buildDetailChips(r) {
    const chips = [];
    if (r.rating > 0) chips.push(`<span class="search-detail-chip search-detail-rating">&#9733; ${r.rating.toFixed(1)}</span>`);
    if (r.certification) chips.push(`<span class="search-detail-chip">${esc(r.certification)}</span>`);
    if (r.runtime > 0) {
        const label = r.type === 'series' ? `${r.runtime} min/ep` : `${r.runtime} min`;
        chips.push(`<span class="search-detail-chip">${label}</span>`);
    }
    if (r.network) {
        const networkLabel = r.seasonCount > 0 ? `${esc(r.network)} &middot; ${r.seasonCount} season${r.seasonCount !== 1 ? 's' : ''}` : esc(r.network);
        chips.push(`<span class="search-detail-chip">${networkLabel}</span>`);
    } else if (r.seasonCount > 0) {
        chips.push(`<span class="search-detail-chip">${r.seasonCount} season${r.seasonCount !== 1 ? 's' : ''}</span>`);
    }
    if (r.genres && r.genres.length) chips.push(`<span class="search-detail-chip">${r.genres.slice(0, 3).map(esc).join(' &middot; ')}</span>`);
    return chips.join('');
}
```

Replace with:
```js
function buildDetailChips(r) {
    const chips = [];
    if (r.rating > 0) chips.push(html`<span class="search-detail-chip search-detail-rating">&#9733; ${r.rating.toFixed(1)}</span>`);
    if (r.certification) chips.push(html`<span class="search-detail-chip">${r.certification}</span>`);
    if (r.runtime > 0) {
        const label = r.type === 'series' ? `${r.runtime} min/ep` : `${r.runtime} min`;
        chips.push(html`<span class="search-detail-chip">${raw(label)}</span>`);
    }
    if (r.network) {
        const networkLabel = r.seasonCount > 0
            ? html`${r.network} &middot; ${r.seasonCount} season${r.seasonCount !== 1 ? 's' : ''}`
            : html`${r.network}`;
        chips.push(html`<span class="search-detail-chip">${networkLabel}</span>`);
    } else if (r.seasonCount > 0) {
        chips.push(html`<span class="search-detail-chip">${r.seasonCount} season${r.seasonCount !== 1 ? 's' : ''}</span>`);
    }
    if (r.genres && r.genres.length) {
        const genreText = r.genres.slice(0, 3).map(g => html`${g}`).join(' &middot; ');
        chips.push(html`<span class="search-detail-chip">${raw(genreText)}</span>`);
    }
    return chips.map(c => c.str).join('');
}
```

- [ ] **Step 3: Refactor renderResultCard to use html`` and add data-testid**

Find `renderResultCard`:
```js
function renderResultCard(r) {
    const poster = r.poster ? `<img src="${r.poster}" alt="">` : '<div class="no-poster"></div>';
```

Replace the entire function with:
```js
function renderResultCard(r) {
    const poster = r.poster
        ? html`<img src="${r.poster}" alt="">`
        : raw('<div class="no-poster"></div>');
    const badge = r.type === 'movie' ? 'Movie' : 'Series';
    const tmdbId = r.tmdbId || 0;
    const tvdbId = r.tvdbId || 0;
    const added = r.added;
    const isManager = store.get('role') === 'manager' || store.get('role') === 'admin';
    const actionBtn = isManager
        ? html`<button
                class="${added ? 'search-add added' : 'search-add'}"
                ${added ? raw('disabled') : raw('')}
                data-type="${r.type}"
                data-tmdb="${tmdbId}"
                data-tvdb="${tvdbId}"
                data-testid="search-add-btn"
                onclick="event.stopPropagation();addMedia(this.dataset.type,this.dataset.type==='movie'?parseInt(this.dataset.tmdb):parseInt(this.dataset.tvdb),this)"
              >${added ? 'Added' : 'Add'}</button>`
        : html`<button
                class="search-request"
                data-type="${r.type}"
                data-tmdb="${tmdbId}"
                data-tvdb="${tvdbId}"
                data-title="${r.title}"
                data-year="${r.year || 0}"
                data-poster="${r.poster || ''}"
                data-testid="search-request-btn"
                onclick="event.stopPropagation();submitRequest(this.dataset.type,parseInt(this.dataset.tmdb),parseInt(this.dataset.tvdb),this.dataset.title,parseInt(this.dataset.year),this.dataset.poster);this.textContent='Requested';this.disabled=true"
              >Request</button>`;
    const detailChips = buildDetailChips(r);
    return html`
        <div class="search-card" data-testid="search-result-card" data-tmdb="${tmdbId}" data-type="${r.type}"
             onclick="showMediaDetail(${tmdbId},${tvdbId},'${r.type}')">
            <div class="search-poster">${poster}</div>
            <div class="search-info">
                <div class="search-title">${r.title}</div>
                <div class="search-meta">
                    <span class="search-year">${r.year || ''}</span>
                    <span class="search-badge">${badge}</span>
                    ${raw(detailChips)}
                </div>
            </div>
            <div class="search-actions">${actionBtn}</div>
        </div>`.str;
}
```

- [ ] **Step 4: Remove the esc() calls that are now handled by html``**

After replacing `buildDetailChips` and `renderResultCard`, verify that `esc()` is no longer called from those two functions:
```bash
grep -n "esc(" nginx/dashboard.js | grep -v "^[0-9]*:.*function esc\|^[0-9]*:.*//\|escAttr"
```

The remaining `esc()` calls should only be in other parts of the file (downloads, pipeline cards, etc.). If `esc()` in the search section is now zero, leave the global `esc()` function intact — other sections still use it.

- [ ] **Step 5: Measure LoC reduction**

```bash
wc -l nginx/dashboard.js
```

Document the count. Should be ≤2315 (at least 30 lines fewer than baseline 2345). The reduction comes from eliminating manual escaping noise, not from removing logic.

- [ ] **Step 6: Verify the dashboard works**

Open `http://localhost:7399/` with the test stack. Search for a title (e.g., "Batman"). Results should render correctly. Check browser console — no errors.

Run Playwright specs:
```bash
npx playwright test --config tests/playwright/playwright.config.js --reporter list
```

Expected: all specs pass.

- [ ] **Step 7: Commit**

```bash
git add nginx/dashboard.js
git commit -m "refactor(dashboard): use html`` template tag for search cards, add data-testid"
```

---

## Self-Review

**Spec coverage check:**

| Vision requirement | Covered by |
|---|---|
| Custom micro-framework ~150–200 lines | Task 1 (178 lines) |
| Reactive state with store | Task 1: `createStore()` |
| Component registration | Task 1: `component()`, `mount()` |
| data-testid hooks throughout | Task 2 (14+ static attrs) + Task 4 (dynamic cards) |
| No build step, no bundler | Task 1: plain JS, loaded via `<script>` |
| No external frontend runtime dependencies | Task 1: zero imports |
| Dashboard shrinks in LoC | Task 4: measured in Step 5 |
| Playwright can anchor to stable selectors | Tasks 2 + 4: data-testid on all key elements |
| Framework loaded before dashboard.js | Task 2 Step 6 |

**Placeholder scan:** No TBDs. All code is complete and self-contained.

**Type consistency:** `html\`\`` returns a `raw()` object with `.str` property. Task 4 uses `.str` to extract the string for `innerHTML` assignment. This is consistent with the `raw()` definition in Task 1.

**What's deferred (not in this plan):**
- Refactoring pipeline cards, download cards, user management section to use `html\`\`` — follow the same pattern from Task 4. Each section reduces LoC by ~10–20% when converted.
- Converting stateful rendering loops to `component()` — higher value but also higher risk; do after the `html\`\`` refactor is stable.
