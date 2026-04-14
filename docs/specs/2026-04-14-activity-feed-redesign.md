# Activity Feed Redesign

**Date:** 2026-04-14
**Status:** Approved

## Problem

The Activity section on the search tab accumulates stale, non-actionable failure events (failed transcodes, old download errors) that have no resolution path. They dominate the feed and can't be cleared, so users are always looking at noise they can't act on.

## Goals

- Old failures stop being "in your face" by default
- Each event is individually dismissable
- Events auto-expire so the feed self-cleans
- Failed events expose enough detail to understand what went wrong + take action

## Design: Tiered List with Inline Drawers

### Layout

Three visual layers in the Activity section:

1. **Active items** — events ≤24h old, undismissed. Rendered as expandable rows at full opacity.
2. **Older separator** — a "N older events ▾" divider row, collapsed by default. Clicking toggles the older section.
3. **Older items** — events >24h old, rendered at 55% opacity. Same drawer behavior. Hard-expired from the backend after 7 days.

### Row anatomy

Each row: `[icon] [message — truncated] [relative time] [✕ dismiss]`

- Clicking the row (anywhere except ✕) toggles an inline drawer open/closed (CSS class toggle, no modal).
- The ✕ button quick-dismisses without opening the drawer.

### Drawer content by type

| Type | Detail shown | Actions |
|------|-------------|---------|
| `content_ready` | Title, media type (from notification fields) | Dismiss |
| `validation_failed` / transcode failed | FFmpeg/validation error message, job ID, stage | Retry · Jump to job · Dismiss |
| `download_failed` (arr history) | Release name, failure reason from arr | Dismiss |
| `storage_warning` / `storage_critical` | Disk path, used/total, threshold | Go to storage · Dismiss |

### Action wiring

- **Dismiss** — `DELETE /api/pelicula/notifications/{id}`, then re-fetch to update both activity feed and bell badge.
- **Retry** — `POST /api/pelicula/procula/jobs/{job_id}/retry` (new proxy endpoint, same pattern as existing `/resub` proxy in `library.go`). Shows `toast()` on success/failure.
- **Jump to job** — `router.navigate('jobs', {id: job_id})`.
- **Go to storage** — `router.navigate('storage')`.

### Auto-expire

- **Frontend threshold:** events >24h go to the "older" section. Constant in `activity.js`, not configurable via UI.
- **Backend prune:** procula's `appendToFeed` (currently caps at 50 events) gets an additional pass to drop events older than 7 days before writing.

---

## Data changes

### `dashNotif` struct (`middleware/hooks.go`)

Add two optional fields:

```go
type dashNotif struct {
    ID        string    `json:"id"`
    Timestamp time.Time `json:"timestamp"`
    Type      string    `json:"type"`
    Message   string    `json:"message"`
    Detail    string    `json:"detail,omitempty"`   // error text / media info for drawer
    JobID     string    `json:"job_id,omitempty"`   // procula job ID; enables Retry + Jump to job
}
```

### Procula `NotificationEvent` (`procula/catalog.go`)

Add `JobID` field, populated in `buildEvent()` from `job.ID`:

```go
type NotificationEvent struct {
    ID        string    `json:"id"`
    Timestamp time.Time `json:"timestamp"`
    Type      string    `json:"type"`
    Title     string    `json:"title"`
    Year      int       `json:"year,omitempty"`
    MediaType string    `json:"media_type"`
    Message   string    `json:"message"`
    Detail    string    `json:"detail,omitempty"`   // error text for drawer
    JobID     string    `json:"job_id,omitempty"`
}
```

`buildEvent()` sets `JobID = job.ID` and `Detail = job.Error` (or empty for `content_ready`).

### `handleNotificationsProxy` (`middleware/hooks.go`)

The proxy already reads procula's feed; extend the decode struct to pass through `detail` and `job_id`.

For arr history events (`fetchArrHistory`), populate `Detail` from the release quality/title info already present in the history record. No `JobID` for arr events.

### New proxy endpoint

`POST /api/pelicula/procula/jobs/{id}/retry` — registered in `middleware/main.go`, implemented in `middleware/library.go` alongside `handleJobResub`. Proxies to `POST /api/procula/jobs/{id}/retry`.

---

## Frontend changes

### New file: `nginx/activity.js`

Registered as a PeliculaFW component (`component('activity', ...)`), mounted by `dashboard.js` alongside `notifications` and `search`. Exports nothing to `window` — all interaction via event delegation on the rendered DOM.

Responsibilities:
- `renderActivity(events)` — splits events into active/older, renders tiered list with drawers
- Dismiss handler — calls `DELETE /api/pelicula/notifications/{id}`, triggers `checkNotifications()` to refresh
- Retry handler — calls `POST /api/pelicula/procula/jobs/{id}/retry`, shows `toast()`
- Navigation handlers — uses `router.navigate()` for Jump to job and Go to storage

### `nginx/dashboard.js`

- Remove the existing `renderActivity()` function and its `window` export.
- Add `PeliculaFW.mount('activity', document.getElementById('activity-section'))` alongside the other component mounts.
- `checkNotifications()` calls the component's `renderActivity` via the store or direct call — same pattern as `renderNotifications`.

### `nginx/styles.css`

New rules alongside existing `.activity-*` block:
- `.act-drawer` — hidden by default, shown when `.open`
- `.act-older` — hidden by default, shown when `.visible`
- `.act-sep` — separator row styling
- `.act-x` — dismiss button
- `.act-btn` — drawer action buttons (primary variant for main action)
- Older items opacity (`.act-older .act-item { opacity: 0.55 }`)

### `nginx/index.html`

No changes — the `#activity-section` div already exists.
