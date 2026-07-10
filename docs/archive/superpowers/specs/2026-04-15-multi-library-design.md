# Multi-Library Support

## Context

Pelicula's media directory structure is hardcoded to two libraries: `movies/` and `tv/`. Users with additional media collections (Anime, YouTube Documentaries, home videos, etc.) have no way to integrate them into the stack. These collections don't fit into movies or TV, may not be managed by Sonarr/Radarr, but should still be served by Jellyfin and optionally processed by Procula.

This design adds support for user-defined libraries beyond the built-in movies/tv pair.

## Library Model

Each library is defined by:

| Field | Type | Description |
|---|---|---|
| `name` | string | Display name (e.g., "Anime") |
| `slug` | string | Directory name / URL-safe key (e.g., `anime`). Auto-generated from name, editable. |
| `path` | string | Host path. Empty = `LIBRARY_DIR/<slug>`. Absolute path = external directory. |
| `type` | enum | `movies` \| `tvshows` \| `mixed` \| `other` — determines Jellyfin library type |
| `arr` | enum | `radarr` \| `sonarr` \| `none` — which *arr manages this library |
| `processing` | enum | `audit` \| `full` \| `off` — Procula behavior (default: `audit` for custom, inherits global setting for built-ins) |

**Built-in libraries:** `movies` (slug: `movies`, type: `movies`, arr: `radarr`) and `tv` (slug: `tv`, type: `tvshows`, arr: `sonarr`) are always present and cannot be deleted. Their processing mode is editable.

**Storage:** `config/pelicula/libraries.json`. Read/written by the settings UI and the CLI.

### Example libraries.json

```json
{
  "libraries": [
    {"name": "Movies", "slug": "movies", "type": "movies", "arr": "radarr", "processing": "full", "builtin": true},
    {"name": "TV Shows", "slug": "tv", "type": "tvshows", "arr": "sonarr", "processing": "full", "builtin": true},
    {"name": "Anime", "slug": "anime", "type": "tvshows", "arr": "sonarr", "processing": "audit"},
    {"name": "YouTube Docs", "slug": "youtube-docs", "type": "other", "arr": "none", "processing": "audit"},
    {"name": "Home Videos", "slug": "home-videos", "path": "/volume2/home-videos", "type": "movies", "arr": "none", "processing": "off"}
  ]
}
```

## Volume Mount Strategy

### Broad mount for LIBRARY_DIR

Instead of mounting each subdirectory individually, mount the entire `LIBRARY_DIR` as `/media` in every service that needs library access.

**Before:**
```yaml
- ${LIBRARY_DIR}/movies:/movies
- ${LIBRARY_DIR}/tv:/tv
```

**After:**
```yaml
- ${LIBRARY_DIR}:/media
```

Container paths change: `/movies` → `/media/movies`, `/tv` → `/media/tv`.

Any new subdirectory created under `LIBRARY_DIR` on the host is immediately visible inside containers at `/media/<slug>` — no compose changes, no restart.

### Services affected

| Service | Old mounts | New mount |
|---|---|---|
| sonarr | `LIBRARY_DIR/tv:/tv` | `LIBRARY_DIR:/media` |
| radarr | `LIBRARY_DIR/movies:/movies` | `LIBRARY_DIR:/media` |
| jellyfin | `LIBRARY_DIR/tv:/data/tv`, `LIBRARY_DIR/movies:/data/movies` | `LIBRARY_DIR:/media` |
| procula | `LIBRARY_DIR/movies:/movies`, `LIBRARY_DIR/tv:/tv` | `LIBRARY_DIR:/media` |
| pelicula-api | `LIBRARY_DIR/movies:/movies`, `LIBRARY_DIR/tv:/tv` | `LIBRARY_DIR:/media` |
| bazarr | `LIBRARY_DIR/movies:/movies`, `LIBRARY_DIR/tv:/tv` | `LIBRARY_DIR:/media` |

WORK_DIR mounts (`/downloads`, `/processing`) are unchanged.

### External paths

Libraries with an absolute host path outside LIBRARY_DIR require additional volume mounts. The CLI generates a `docker-compose.libraries.yml` override file:

```yaml
# Auto-generated — do not edit
services:
  jellyfin:
    volumes:
      - /volume2/home-videos:/media/home-videos
  pelicula-api:
    volumes:
      - /volume2/home-videos:/media/home-videos:ro
  # ... repeated for each service that needs access
```

The CLI passes `-f docker-compose.yml -f docker-compose.libraries.yml` to compose when this file exists. Adding an external library requires a stack restart; the CLI/UI communicates this clearly.

## Code Migration

### Core principle

Replace every hardcoded `/movies` and `/tv` path literal with a registry lookup. A shared helper provides paths:

```go
// In middleware
func (lib Library) ContainerPath() string { return "/media/" + lib.Slug }

// In procula (reads library config from pelicula-api or local config)
func libraryPath(slug string) string { return "/media/" + slug }
```

### Middleware (pelicula-api) changes

| Location | Current | After |
|---|---|---|
| `library.go` `browseRoots()` | Returns `["/movies", "/tv", "/downloads"]` | Returns `/media/<slug>` for each library + `/downloads` |
| `library.go` `suggestedMoviePath/TVPath()` | Hardcoded `/movies/...` and `/tv/...` | `suggestedPath(library)` → `/media/<slug>/...` |
| `library.go` path validation | Accepts only `/movies`, `/tv` | Accepts `/media/<slug>` for any registered library |
| `autowire.go` `wireRootFolder()` | Wires `/tv` and `/movies` only | Wires `/media/<slug>` for each arr-managed library |
| `autowire.go` `wireJellyfinLibrary()` | Creates "Movies" and "TV Shows" | Creates a Jellyfin library for each registered library |
| `search.go` rootFolderPath defaults | `/movies` for Radarr, `/tv` for Sonarr | Looks up default root folder from library registry |
| `hooks.go` `isAllowedWebhookPath()` | Allows `/downloads`, `/movies`, `/tv`, `/processing` | Allows `/downloads`, `/processing`, `/media/*` |
| `host.go` disk stats fallback | Falls back to `/movies` | Falls back to `/media` |
| `export.go` rootFolderPath | Hardcoded `/movies`, `/tv` | From library registry |
| `peligrosa/requests.go` | `REQUESTS_RADARR_ROOT`, `REQUESTS_SONARR_ROOT` defaults | Defaults to first radarr/sonarr library from registry |

### Procula changes

| Location | Current | After |
|---|---|---|
| `pipeline.go` `isAllowedMediaPath()` | `/downloads`, `/movies`, `/tv`, `/processing` | `/downloads`, `/processing`, `/media/*` |
| `pipeline.go` `isAllowedPath()` (delete safety) | `/downloads`, `/processing` | Unchanged — delete safety stays narrow |
| `main.go` `isLibraryPath()` | `/movies` or `/tv` | Starts with `/media/` |
| `actions.go` `arrTypeFromPath()` | `/tv/` → sonarr, else radarr | Looks up library registry by path prefix |
| `storage.go` monitoring | Four hardcoded paths | `/downloads`, `/media`, `/processing` |

### CLI (cmd/pelicula/) changes

| Location | Current | After |
|---|---|---|
| `dirs.go` `setupDirs()` | Creates `movies/`, `tv/` under LIBRARY_DIR | Creates directory for each registered library |
| `cmd_up.go` | Passes HOST_LIBRARY_DIR to setup | Also passes library config if it exists |
| `seed.go` qBt categories | Unchanged | Unchanged — downloads are per-arr, not per-library |

### Import wizard (nginx/import.js)

| Location | Current | After |
|---|---|---|
| `rootFolderPath` assignment | Hardcoded `/movies` or `/tv` | Selected from library dropdown based on match type |
| Library selection | None | Dropdown populated from `/api/pelicula/libraries` |

## Autowire Integration

### Sonarr/Radarr root folders

For each library with `arr != "none"`:
- Call `wireRootFolder()` with path `/media/<slug>`
- This registers the directory as a root folder in the appropriate *arr app
- Multiple libraries can map to the same *arr (e.g., both "TV" and "Anime" → Sonarr)

### Jellyfin libraries

For each registered library:
- Call `wireJellyfinLibrary()` with:
  - Name: library display name
  - Collection type: mapped from library `type` field (`movies` → `movies`, `tvshows` → `tvshows`, `mixed` → `mixed`, `other` → `mixed`)
  - Path: `/media/<slug>`

### qBittorrent categories

Unchanged. Download categories (`radarr`, `tv-sonarr`) are tied to the *arr app, not to individual libraries. When Sonarr manages both "TV" and "Anime", both use the same download category.

## Settings UI

### New "Libraries" section in admin settings

- Displays current libraries as a list/table
- Built-in libraries (movies, tv) shown with a lock icon — processing mode editable, everything else read-only
- Each custom library has edit and delete actions
- "Add Library" button opens an inline form:
  1. Name (required) — auto-generates slug
  2. Type dropdown: Movies, TV Shows, Mixed, Other
  3. Arr integration: Radarr, Sonarr, None
  4. Processing mode: Audit (default), Full, Off
  5. External path (optional) — if blank, uses `LIBRARY_DIR/<slug>`
- Submit → POST to `/api/pelicula/libraries` → writes config, creates directory, wires services
- If external path → UI shows "Stack restart required" message

### New API endpoints

- `GET /api/pelicula/libraries` — returns all registered libraries
- `POST /api/pelicula/libraries` — add a new library
- `PUT /api/pelicula/libraries/<slug>` — update a library
- `DELETE /api/pelicula/libraries/<slug>` — remove a custom library (built-ins rejected)

## Setup Wizard Changes

After the media directory / path section, add an optional "Additional Libraries" section:

- Shows Movies and TV Shows as pre-filled, non-removable rows
- "Add Library" button allows defining extra libraries during first-time setup
- Same fields as the settings UI form
- Optional — most users proceed with defaults and add libraries later
- Submit handler writes both `.env` and initial `libraries.json`

## Procula Processing Modes

| Mode | Behavior |
|---|---|
| `audit` | Validate files (FFprobe). Flag issues in the job queue. No modifications. User reviews flagged items in the dashboard. |
| `full` | Full pipeline: validate → transcode (if needed) → catalog → notify. Same as current movies/tv behavior. |
| `off` | Procula ignores this library entirely. Files are served by Jellyfin as-is. |

**Default for custom libraries:** `audit`. This is non-destructive — Procula scans and reports but doesn't touch files. Users can promote to `full` once they trust the pipeline for their content.

**Import-time override:** When importing files into a library, the import wizard shows the library's processing profile and lets the user toggle transcoding for that batch.

## Migration Path (Existing Installs)

1. On `pelicula up`, the CLI checks for `libraries.json`
2. If absent, generates a default with the two built-in libraries
3. Compose volume mounts are updated (the compose file change ships with the code update)
4. Existing Jellyfin libraries and *arr root folders continue working — autowire is idempotent and updates paths
5. The old `/movies` and `/tv` container paths no longer exist — autowire removes stale root folders pointing to old paths and creates new ones at `/media/movies` and `/media/tv`

## Verification

1. **Unit test:** Library registry CRUD, path resolution, slug generation
2. **E2e test:** `pelicula test` — verify the two default libraries work under the new mount scheme
3. **Manual test — add custom library:**
   - Add "Anime" library via settings UI
   - Verify directory created under LIBRARY_DIR
   - Verify Jellyfin library appears
   - Verify Sonarr root folder added (if arr = sonarr)
   - Import a file into the anime library
   - Verify Procula flags it in audit mode
4. **Manual test — external path:**
   - Add library with external host path
   - Verify CLI generates compose override
   - Verify restart message shown
   - After restart, verify library accessible
5. **Migration test:**
   - Start with existing install (movies/tv populated)
   - Update code, run `pelicula up`
   - Verify libraries.json auto-generated
   - Verify Jellyfin and *arr root folders updated to /media/* paths
   - Verify existing media still accessible
