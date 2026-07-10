# Library & Storage UX Consolidation

## Context

Library management in Pelicula is currently scattered across five entry points: the storage tab's folder list, the storage tab's volume bars, the Storage Explorer browse tree, the settings tab's libraries panel, and the setup wizard. Each has its own UI for adding libraries, with inconsistent feature coverage (the settings panel has all fields; the storage modal omits processing mode and external paths). Discovery of unregistered folders happens implicitly via "+ Library" buttons sprinkled across views, but there's no deliberate "here's what you could register" experience.

This design consolidates library management into a single **Libraries lane** within the storage tab, adds smart discovery of unregistered media folders, and removes all redundant entry points.

## Design

### Libraries Lane

A new `um-lane` inside `#storage-section`, positioned as the **first lane** (above Volumes). Data is sourced by fetching both `GET /api/pelicula/storage` (for folder sizes and `has_media` flags) and `GET /api/pelicula/libraries` (for library config), then merging by slug. It renders three groups:

**Registered libraries** — Each row shows:
- Color dot (matching the stacked bar palette), display name, type badge (`movies` / `tvshows` / `mixed` / `other`), arr badge, size, percentage of total library usage
- An **Edit** button that expands the row inline to show the library form

**Discovered folders** — Unregistered `/media/*` subdirectories where `has_media: true` in the storage report. Each row shows:
- Dimmed dot, folder name (italic), size
- A **"Register as Library"** button that expands to the inline form with smart defaults pre-filled

**Create new** — A **"+ New Library"** button at the bottom. Opens the same inline form but blank, for creating an empty library from scratch (creates the directory under `/media`).

If there are no discovered folders, that section is simply absent — no empty state.

### Library Form (Inline Expandable)

A single form component used for create, register, and edit. Fields:

| Field | Label | Behavior |
|-------|-------|----------|
| Name | **Name** | Free text. Pre-filled from folder name (title-cased) when registering. |
| Slug | **Folder name** | Auto-derived from name. Shown as read-only with hint *(creates /media/slug)*. Small "edit" link reveals the field for manual override. |
| Type | **Type** | Dropdown: Movies, TV Shows, Mixed, Other. Auto-guessed from folder name patterns (e.g., "anime" → TV Shows, "films" → Movies). |
| Arr | **Managed by** | Dropdown: Radarr, Sonarr, None. Auto-bound from type (movies→Radarr, tvshows→Sonarr, other→None). |
| Processing | **Processing** | Dropdown: Full, Audit, Off. Default: audit for discovered folders, full for new libraries. |
| External Path | **External path** | Hidden behind an "Advanced" toggle. For future use with network drives. When set, the library maps an external host path instead of a subdirectory of LIBRARY_DIR. |

The form has **Save** and **Cancel** buttons. For registered libraries, a **Delete** button (hidden for built-in libraries).

### Backend: `has_media` Flag

The only backend change is in `procula/storage.go`. The `FolderSize` struct gets a new field:

```go
HasMedia bool `json:"has_media,omitempty"`
```

During `computeFolderSizes()`, the file walk already touches every file. For unregistered folders, check if any file has a video extension (`.mkv`, `.mp4`, `.avi`, `.m4v`, `.ts`, `.wmv`, `.mov`, `.flv` — same list used by the browse endpoint in `middleware/library.go`). Set `HasMedia: true` on the first match. This is cheap since we're already walking the tree, and we can short-circuit after the first video file is found.

### Cleanup: Removed UI

1. **`addLibraryFromStorage()` modal** in `dashboard.js` — replaced by the inline form in the Libraries lane
2. **"+ Library" buttons** in `renderStorageFolders()` and `renderStorage()` in `dashboard.js` — the Libraries lane handles discovery now
3. **"+ Library" buttons** in the Storage Explorer browse tree (`import.js`) — browse tree shows library badges only (read-only)
4. **Libraries panel** in the Settings tab (`settings.js` library CRUD functions + `index.html` `#st-libraries-panel`) — all library management moves to the storage tab

### Storage Explorer Adjustments

The Storage Explorer stays as a collapsible section within the storage tab. Changes:
- Remove "+ Library" buttons from the browse tree
- Keep library badges on registered `/media/*` directories (read-only indicator)
- Import scan/match/apply flow is unchanged — targets existing libraries as destinations
- `/import` URL continues to redirect to `/#storage-explorer`

### What's NOT Changing

- Library CRUD API (`POST/PUT/DELETE /api/pelicula/libraries`) — unchanged
- Browse API (`GET /api/pelicula/browse`) — unchanged
- Setup wizard library creation — uses its own code path, unaffected
- Procula's library cache refresh loop — unchanged
- Docker compose override for external paths — unchanged
- Default library definitions (the 3-file duplication) — out of scope

## Key Files

| File | Change |
|------|--------|
| `procula/storage.go` | Add `HasMedia` field to `FolderSize`, detect video files during walk |
| `nginx/index.html` | Add Libraries lane HTML inside `#storage-section`, remove `#st-libraries-panel` from settings |
| `nginx/dashboard.js` | Add `renderLibrariesLane()`, inline library form, remove `addLibraryFromStorage()` modal and scattered "+ Library" buttons |
| `nginx/import.js` | Remove "+ Library" buttons from browse tree |
| `nginx/settings.js` | Remove library panel code (`loadLibraries`, `renderLibraries`, `addLibrary`, `deleteLibrary`) |

## Verification

1. **Backend**: Add `has_media` to procula storage report. Verify via `curl /api/procula/storage` that unregistered folders with video files show `has_media: true`
2. **Discovery**: Create a test folder under `/media` with a `.mkv` file. Verify it appears as "Discovered" in the Libraries lane
3. **Register**: Register the discovered folder via the inline form. Verify it moves to "Registered" group, storage bars update, and procula's library cache picks it up
4. **Edit/Delete**: Edit a custom library's type/arr. Verify persistence. Delete a custom library. Verify built-ins reject deletion
5. **Create new**: Create a library from scratch via "+ New Library". Verify the directory is created under `/media` and the library appears in both the lane and the storage report
6. **Cleanup**: Verify no "+ Library" buttons remain in Volumes lane, By-folder lane, or Storage Explorer browse tree. Verify Settings tab no longer has a Libraries panel
7. **Import flow**: Open Storage Explorer, browse/scan/apply. Verify it works without the removed buttons
8. **E2E**: Run `pelicula test`
