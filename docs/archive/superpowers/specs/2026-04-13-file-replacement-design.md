# Manual File Replacement

**Date:** 2026-04-13
**Status:** Approved

## Problem

Files can land in the library with wrong audio language (e.g. an episode imported in Italian instead of English). There is currently no way to remove such a file, blocklist the specific release in Sonarr/Radarr, and trigger a fresh search — without doing it manually through the *arr UIs.

## Goal

A manual "Replace" action accessible from any catalog item's context menu. The user selects a scope (episode/season/series), optionally enters a reason, and confirms. Pelicula deletes the file(s), blocklists the original release(s) in *arr so they won't be grabbed again, and immediately triggers a new search.

Blocked releases are persisted in pelicula's own DB and can be unblocked from the Settings tab if the action was a mistake.

## Out of Scope (Phase 1)

- Automatic detection of wrong-language files
- Audio language metadata in the catalog (Phase 2 — see below)
- Reason field is optional and logged but not required

---

## Data Model

New table in procula's SQLite DB: `blocked_releases`

| column | type | notes |
|---|---|---|
| `id` | INTEGER PK | |
| `arr_app` | TEXT | `sonarr` or `radarr` |
| `arr_blocklist_id` | INTEGER | Entry ID from *arr's blocklist — needed to reverse the block |
| `arr_item_id` | INTEGER | Series or movie ID in *arr |
| `display_title` | TEXT | Human-readable label, e.g. `Silo S01E01` |
| `file_path` | TEXT | Original path before deletion |
| `blocked_at` | DATETIME | |
| `reason` | TEXT | Optional, from the drawer |

After POSTing to *arr's `history/failed`, pelicula queries `GET /api/v3/blocklist` filtered by item to retrieve the new blocklist entry ID. That ID is stored as `arr_blocklist_id` and is what enables a clean unblock via `DELETE /api/v3/blocklist/{id}`.

---

## Backend

### Procula Action: `replace`

Registered in procula's action registry alongside `validate`, `transcode`, `subtitle_search`, and `dualsub`.

**Request:**
```
POST /api/procula/actions
{
  "action": "replace",
  "target": "/tv/Silo/Season 01/Silo S01E01.mkv",
  "params": {
    "scope": "episode",
    "reason": "Italian audio"
  }
}
```

`scope` values: `episode`, `season`, `series`, `movie`. For `season` and `series`, procula resolves sibling file paths from the catalog DB before processing.

**Execution sequence per file:**

1. Look up the file's *arr item via a middleware path-lookup endpoint
2. Query *arr history for that episode/movie — find the most recent `downloadFolderImported` event to get the `historyId`
3. `POST /api/v3/history/failed/{historyId}` — blocklists the release in *arr
4. Query *arr blocklist to retrieve the new `arr_blocklist_id`; write row to `blocked_releases`
5. Delete the file from disk
6. `POST /api/v3/command` `RescanSeries` / `RescanMovie` — *arr marks the item as missing
7. `POST /api/v3/command` `EpisodeSearch` / `MoviesSearch` — triggers a fresh grab

The action runs synchronously (supports `?wait=N`). All files within the scope are processed before returning.

### New Procula Endpoints

- `GET /api/procula/blocked-releases` — list all rows in `blocked_releases`, newest first
- `DELETE /api/procula/blocked-releases/{id}` — remove the block: calls `DELETE /api/v3/blocklist/{arr_blocklist_id}` in *arr, then deletes the local row. Does not re-add the file or trigger a new search.

### New Middleware Endpoint

- `GET /api/pelicula/catalog/by-path?path=...` — resolves a file path to its *arr app, item ID, and episode/movie ID. Required by the replace action to look up history. (May already exist in some form — verify before adding.)

---

## Frontend

### Context Menu

New "Replace..." entry on all catalog items (movies and TV episodes). Not gated on flags — available on any item.

### Confirmation Drawer

Opens on "Replace..." click. Contains:

- **Item title + path** — read-only, for confirmation
- **Scope selector** — TV only; hidden for movies (scope is always `movie`):
  - This episode
  - Entire season (Season N)
  - Entire series
- **File count preview** — "This will replace N files" when season/series is selected
- **Reason field** — optional free text
- **"Replace & Re-search" button** — disabled until scope is confirmed; triggers the action
- **Cancel button**

On confirm: POST to `/api/procula/actions`, show inline spinner while the synchronous action runs, then close drawer and show toast: "Replacement queued — Sonarr is searching for a new release."

### Blocked Releases (Settings Tab)

New section in the Settings tab listing all rows from `GET /api/procula/blocked-releases`:

- Columns: title, file path, date blocked, reason (if set)
- **Unblock** button per row — calls `DELETE /api/procula/blocked-releases/{id}`
- Unblocking removes the *arr blocklist entry so that release could be re-grabbed if it surfaces again; it does not restore the deleted file or trigger a search

---

## Phase 2: Audio Language Detection

Extend `extractCodecs()` in `procula/validate.go` to read the `language` tag from each audio stream (the same way subtitle languages are already read). Store as `AudioTracks []AudioTrack` in `CodecInfo`, where each track carries codec name + language code.

Benefits:
- The replace drawer can show "Detected audio: it (Italian), en missing" before the user confirms
- Opens the door to automatic flagging of wrong-language files in the future
- No pipeline behavior changes required for Phase 2 — detection only

---

## Key Files

| File | Change |
|---|---|
| `procula/actions.go` | Register `replace` action |
| `procula/pipeline.go` | Extract `blocklist()` logic for reuse; add path→*arr lookup call |
| `procula/db.go` | Add `blocked_releases` migration |
| `procula/validate.go` | (Phase 2) Extend `extractCodecs()` for audio language tags |
| `procula/queue.go` | (Phase 2) Add `AudioTracks []AudioTrack` to `CodecInfo` |
| `middleware/` | Add `GET /api/pelicula/catalog/by-path` if not present |
| `nginx/catalog.css` / dashboard JS | Context menu entry, drawer, settings section |
