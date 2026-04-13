# Logs Tab Redesign

**Date:** 2026-04-13
**Status:** Approved — ready for implementation

## Summary

Improve the Logs tab with a three-column grid layout (timestamp · service · message), date separators at day boundaries, slightly smaller font, and scroll-anchor auto-scroll that pauses when the user scrolls up.

## Design Decisions

### Layout

Each log row is a CSS grid with three columns:

| Column | Width | Style |
|--------|-------|-------|
| Timestamp | `7ch` | `HH:MM:SS`, right-aligned, dim `#555` |
| Service badge | `14ch` | Service name, service color + `color-mix` tinted background pill |
| Message | `1fr` | Inherits service color from row |

### Font size

`0.65rem` (down from `0.72rem`).

### Date separators

- A left-aligned `Apr 13` label with a bottom border (`1px solid #1e2028`) is inserted:
  - Once at the top of every rendered view (for the first entry's date)
  - Again whenever the calendar date changes between consecutive entries
- Entries with no parseable timestamp (zero `time.Time`) render with a blank timestamp cell and do **not** affect date separator tracking.

### Auto-scroll

- On each SSE push, scroll the log container to the bottom **unless** the user has manually scrolled up.
- Resume auto-scroll when the user scrolls back within 30px of the bottom.
- Implemented via a `scroll` event listener on the `#logs-stream` container that sets/clears a `userScrolled` flag.

## Files Changed

### `nginx/index.html`

- Change `<pre id="logs-stream" class="logs-stream">` → `<div id="logs-stream" class="logs-stream">` (pre's `white-space` model conflicts with CSS grid).

### `nginx/catalog.css`

Replace the existing `.logs-stream`, `.logs-line`, and `.logs-line-svc` rules:

```css
.logs-stream {
    max-height: 60vh; overflow-y: auto;
    background: #0c0e13; padding: 0.5rem; border-radius: 8px;
    font-family: ui-monospace, Menlo, monospace; font-size: 0.65rem;
    word-break: break-word;
}
.logs-date-sep {
    color: #555; font-size: 0.58rem; letter-spacing: 0.08em;
    text-transform: uppercase;
    padding: 0.25rem 0 0.1rem;
    border-bottom: 1px solid #1e2028;
    margin-bottom: 0.15rem;
    /* spans all 3 grid columns when inside a logs-line sibling context */
}
.logs-line {
    display: grid;
    grid-template-columns: 7ch 14ch 1fr;
    gap: 0 0.6rem;
    line-height: 1.6;
    align-items: baseline;
}
.logs-line-ts {
    color: #555;
    text-align: right;
    flex-shrink: 0;
}
.logs-line-svc {
    background: color-mix(in srgb, currentColor 8%, transparent);
    padding: 0 0.3rem;
    border-radius: 3px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
.logs-line-msg {
    /* inherits service color from .logs-svc-{name} on parent */
    white-space: pre-wrap;
    word-break: break-word;
    min-width: 0;
}
/* color-by-service rules unchanged */
```

### `nginx/logs.js`

**`renderLogs(out, entries)`** — rebuild to emit date separators and three-column rows:

```js
function renderLogs(out, entries) {
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

**Auto-scroll initialization** — attach once when the tab first activates:

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

Call `initScrollAnchor(out)` from `renderLogs` (or `onTab`) before rendering.

**`logsState`** — add `userScrolled: false` field.

## Out of Scope

- Virtualized rendering (not needed at 200-line cap)
- Text search / filtering within lines
- Log level coloring within message text
