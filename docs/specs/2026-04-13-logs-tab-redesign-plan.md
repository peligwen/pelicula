# Logs Tab Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign the Logs tab with a three-column CSS grid layout (timestamp, service badge, message), date separators at day boundaries, and scroll-anchor auto-scroll that pauses when the user scrolls up.

**Architecture:** Three files change: one HTML element swap (`<pre>` to `<div>`), CSS rule replacements for grid layout and new date-separator styling, and a JS rewrite of `renderLogs` plus new `initScrollAnchor` function. The per-service color classes (`.logs-svc-sonarr` etc.) remain unchanged and drive `currentColor` inheritance for both the badge tint and message text.

**Tech Stack:** Vanilla HTML, CSS (grid, `color-mix`), JavaScript (DOM API, SSE integration)

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `nginx/index.html` (line 229) | Modify | Swap `<pre>` to `<div>` for the logs container |
| `nginx/catalog.css` (lines 307-317) | Modify | Replace `.logs-stream`, `.logs-line`, `.logs-line-svc` rules; add `.logs-date-sep`, `.logs-line-ts`, `.logs-line-msg` |
| `nginx/logs.js` (full file) | Modify | Rewrite `renderLogs`, add `initScrollAnchor`, add `userScrolled` to `logsState` |

---

### Task 1: Change `<pre>` to `<div>` in index.html

**Files:**
- Modify: `nginx/index.html:229`

- [ ] **Step 1: Replace the pre element**

Change line 229 from:

```html
                <pre id="logs-stream" class="logs-stream"></pre>
```

to:

```html
                <div id="logs-stream" class="logs-stream"></div>
```

This is necessary because `<pre>` applies `white-space: pre` by default, which conflicts with CSS grid layout on child elements.

- [ ] **Step 2: Commit**

```bash
git add nginx/index.html
git commit -m "refactor(logs): swap <pre> to <div> for grid layout compatibility"
```

---

### Task 2: Replace CSS rules for grid layout and add new classes

**Files:**
- Modify: `nginx/catalog.css:307-317`

- [ ] **Step 1: Replace `.logs-stream` rule (lines 307-312)**

Replace the existing `.logs-stream` block:

```css
.logs-stream {
    max-height: 60vh; overflow: auto;
    background: #0c0e13; padding: 0.5rem; border-radius: 8px;
    font-family: ui-monospace, Menlo, monospace; font-size: 0.72rem;
    white-space: pre-wrap; word-break: break-word;
}
```

with:

```css
.logs-stream {
    max-height: 60vh; overflow-y: auto;
    background: #0c0e13; padding: 0.5rem; border-radius: 8px;
    font-family: ui-monospace, Menlo, monospace; font-size: 0.65rem;
    word-break: break-word;
}
```

Key changes: `overflow: auto` becomes `overflow-y: auto`; font-size drops from `0.72rem` to `0.65rem`; `white-space: pre-wrap` is removed.

- [ ] **Step 2: Replace `.logs-line` rule (line 313)**

Replace:

```css
.logs-line { display: block; }
```

with:

```css
.logs-line {
    display: grid;
    grid-template-columns: 7ch 14ch 1fr;
    gap: 0 0.6rem;
    line-height: 1.6;
    align-items: baseline;
}
```

- [ ] **Step 3: Replace `.logs-line-svc` rule (lines 314-317)**

Replace:

```css
.logs-line-svc {
    display: inline-block; width: 10ch; margin-right: 0.6rem;
    color: var(--muted); text-align: right;
}
```

with:

```css
.logs-line-svc {
    background: color-mix(in srgb, currentColor 8%, transparent);
    padding: 0 0.3rem;
    border-radius: 3px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
```

The `color-mix` trick uses `currentColor` (inherited from the `.logs-svc-*` class on the parent row) to produce a subtle tinted background without any per-service background rules.

- [ ] **Step 4: Add new `.logs-date-sep` rule**

Insert immediately after `.logs-stream` and before `.logs-line`:

```css
.logs-date-sep {
    color: #555; font-size: 0.58rem; letter-spacing: 0.08em;
    text-transform: uppercase;
    padding: 0.25rem 0 0.1rem;
    border-bottom: 1px solid #1e2028;
    margin-bottom: 0.15rem;
}
```

- [ ] **Step 5: Add new `.logs-line-ts` rule**

Insert after `.logs-line` and before `.logs-line-svc`:

```css
.logs-line-ts {
    color: #555;
    text-align: right;
    flex-shrink: 0;
}
```

- [ ] **Step 6: Add new `.logs-line-msg` rule**

Insert after `.logs-line-svc`:

```css
.logs-line-msg {
    white-space: pre-wrap;
    word-break: break-word;
    min-width: 0;
}
```

`min-width: 0` is required so the grid column can shrink below its content's intrinsic width.

- [ ] **Step 7: Verify the per-service color rules are untouched**

Confirm lines 318-328 (the `.logs-svc-sonarr` through `.logs-svc-pelicula-api` rules) remain exactly as they are. Do not modify them.

- [ ] **Step 8: Commit**

```bash
git add nginx/catalog.css
git commit -m "style(logs): three-column grid layout with date-sep and color-mix badge"
```

---

### Task 3: Add `userScrolled` to `logsState` and implement `initScrollAnchor`

**Files:**
- Modify: `nginx/logs.js:12-16` (logsState object)
- Modify: `nginx/logs.js` (add new function)

- [ ] **Step 1: Add `userScrolled` field to `logsState`**

Change the `logsState` object from:

```js
const logsState = {
    loaded: false,
    loading: false,
    enabled: new Set(ALL_SERVICES),
    lastEntries: [],
};
```

to:

```js
const logsState = {
    loaded: false,
    loading: false,
    enabled: new Set(ALL_SERVICES),
    lastEntries: [],
    userScrolled: false,
};
```

- [ ] **Step 2: Add the `initScrollAnchor` function**

Insert this function after the `lfetch` function and before `loadLogs`:

```js
function initScrollAnchor(out) {
    if (out._scrollListenerAttached) return;
    out._scrollListenerAttached = true;
    out.addEventListener('scroll', () => {
        const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 30;
        logsState.userScrolled = !atBottom;
    });
}
```

The guard flag `_scrollListenerAttached` on the DOM element ensures the listener is attached exactly once.

- [ ] **Step 3: Commit**

```bash
git add nginx/logs.js
git commit -m "feat(logs): add userScrolled state and scroll-anchor listener"
```

---

### Task 4: Rewrite `renderLogs` for three-column grid and date separators

**Files:**
- Modify: `nginx/logs.js:41-56` (renderLogs function)

- [ ] **Step 1: Replace the entire `renderLogs` function**

Replace the existing `renderLogs`:

```js
function renderLogs(out, entries) {
    const frag = document.createDocumentFragment();
    for (const e of entries) {
        if (!logsState.enabled.has(e.service)) continue; // client-side service filter
        const row = document.createElement('span');
        row.className = 'logs-line logs-svc-' + e.service;
        const svc = document.createElement('span');
        svc.className = 'logs-line-svc';
        svc.textContent = e.service;
        row.appendChild(svc);
        row.appendChild(document.createTextNode(e.line + '\n'));
        frag.appendChild(row);
    }
    out.replaceChildren(frag);
    out.scrollTop = out.scrollHeight;
}
```

with:

```js
function renderLogs(out, entries) {
    initScrollAnchor(out);
    const frag = document.createDocumentFragment();
    let lastDate = null;
    for (const e of entries) {
        if (!logsState.enabled.has(e.service)) continue;

        // date separator
        const ts = e.ts ? new Date(e.ts) : null;
        const dateStr = ts && !isNaN(ts) ? ts.toLocaleDateString('en-US', {month:'short', day:'numeric'}) : null;
        if (dateStr && dateStr !== lastDate) {
            const sep = document.createElement('div');
            sep.className = 'logs-date-sep';
            sep.textContent = dateStr;
            frag.appendChild(sep);
            lastDate = dateStr;
        }

        const row = document.createElement('div');
        row.className = 'logs-line logs-svc-' + e.service;

        const tsEl = document.createElement('span');
        tsEl.className = 'logs-line-ts';
        tsEl.textContent = ts && !isNaN(ts)
            ? ts.toLocaleTimeString('en-US', {hour:'2-digit', minute:'2-digit', second:'2-digit', hour12:false})
            : '';

        const svc = document.createElement('span');
        svc.className = 'logs-line-svc';
        svc.textContent = e.service;

        const msg = document.createElement('span');
        msg.className = 'logs-line-msg';
        msg.textContent = e.line;

        row.append(tsEl, svc, msg);
        frag.appendChild(row);
    }
    out.replaceChildren(frag);
    if (!logsState.userScrolled) out.scrollTop = out.scrollHeight;
}
```

Key behavioral notes:
- `initScrollAnchor(out)` is called at the top of every render; the guard flag makes it idempotent.
- Entries where `e.ts` is falsy produce a `null` ts — blank timestamp cell, no effect on `lastDate`.
- The first entry with a valid date always gets a separator because `lastDate` starts as `null`.
- Auto-scroll only fires when `!logsState.userScrolled`.

- [ ] **Step 2: Commit**

```bash
git add nginx/logs.js
git commit -m "feat(logs): three-column grid rendering with date separators and auto-scroll"
```

---

### Task 5: Verification

- [ ] **Step 1: Run the Go build**

```bash
cd /Users/gwen/workspace/pelicula && go build ./...
```

Expected: successful build (confirms no accidental .go file breakage).

- [ ] **Step 2: Run tests**

```bash
cd /Users/gwen/workspace/pelicula && go test ./...
```

Expected: all tests pass.

- [ ] **Step 3: Visual spot-check checklist**

Open the app in a browser, navigate to the Logs tab, and verify:

1. Log lines render in a three-column grid: timestamp (right-aligned, dim), service badge (colored pill with subtle tinted background), message.
2. Timestamps display as `HH:MM:SS` in 24-hour format.
3. Date separators appear at the top and at day-boundary transitions, styled as small uppercase text with a subtle bottom border.
4. Service badge background shows a subtle tint of the service color.
5. Scrolling up pauses auto-scroll; new SSE pushes do NOT jump to the bottom while scrolled up.
6. Scrolling back within 30px of the bottom resumes auto-scroll.
7. Font is visibly smaller than before.
8. Long message lines wrap within their grid column without overflowing.
9. Service filter chips still work.

- [ ] **Step 4: Final commit if any spot-check fixes needed**

```bash
git add nginx/index.html nginx/catalog.css nginx/logs.js
git commit -m "fix(logs): address visual issues found during spot-check"
```
