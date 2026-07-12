# API Reference

All `pelicula-api` endpoints are proxied through nginx at `/api/pelicula/`. Internal endpoints are restricted to Docker networks in nginx config — not reachable from the LAN.

All mutating endpoints require an admin or manager session. A session is either cookie-based (from `POST /api/pelicula/auth/login`) or the loopback auto-session granted to requests from the host machine — see [docs/PELIGROSA.md](PELIGROSA.md#loopback-auto-session-middlewarepeligrosaloopbackgo).

Auth levels: **Admin** = session with admin role; **Manager+** = manager or admin session; **Viewer+** = any authenticated session; **Public** = no auth required; **Internal** = Docker-network-only.

## Stability Policy

**Stable since v0.1.** All endpoints below are part of the public API contract:

- **Fields are additive only** — response fields are never removed or renamed
- **New endpoints may be added** in minor releases
- **Breaking changes** (field removal, type changes, endpoint removal) only at major version bumps
- **Frontend treats unknown fields as ignorable** — new fields won't break older dashboard versions

## Endpoint catalog

### Setup mode (pre-.env)

These endpoints are only registered when `SETUP_MODE=true` (i.e., when no `.env` file exists). They run on a separate mux; once the wizard completes the container is replaced by the normal stack.

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/health` | Public | Returns `{"status":"setup"}` in setup mode |
| `GET` | `/api/pelicula/setup/detect` | Public | Returns host platform, timezone, UID/GID suggestions for the wizard |
| `POST` | `/api/pelicula/setup` | Public, CSRF-strict | Validates wizard inputs, generates `.env`, creates directories. Requires local Origin header |

### Authentication

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `POST` | `/api/pelicula/auth/login` | Public | Authenticate with Jellyfin credentials; sets session cookie. **Rate-limited** (10 r/m, burst=5) |
| `POST` | `/api/pelicula/auth/logout` | Public (handler-gated) | Clears session cookie |
| `GET` | `/api/pelicula/auth/check` | Public (handler-gated) | Returns `{authenticated, role, username}`. Used by nginx `auth_request` subrequest (`/auth-check`) |

### Registration and invites

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/register/check` | Public | Returns `{open_registration: bool}` — whether open registration is enabled |
| `GET` | `/api/pelicula/generate-password` | Public | Returns a random passphrase suggestion. **Rate-limited** (10 r/m, burst=5) |
| `POST` | `/api/pelicula/register` | Public, CSRF-strict | Open registration — create viewer account without invite token. Requires local Origin. **Rate-limited** (10 r/m, burst=3) |
| `GET` | `/api/pelicula/invites` | Admin, CSRF-soft | List active invite links |
| `POST` | `/api/pelicula/invites` | Admin, CSRF-soft | Create invite link |
| `GET` | `/api/pelicula/invites/{token}/check` | Public | Check invite validity. **Rate-limited** (10 r/m, burst=5) |
| `POST` | `/api/pelicula/invites/{token}/redeem` | Public (invite-gated) | Self-service viewer registration via invite token. **Rate-limited** (10 r/m, burst=5) |
| `POST` | `/api/pelicula/invites/{token}/revoke` | Admin, CSRF-soft | Revoke (deactivate) an active invite |
| `DELETE` | `/api/pelicula/invites/{token}` | Admin, CSRF-soft | Hard-delete an invite record |

### Operators (role management)

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/operators` | Admin, CSRF-soft | List all pelicula role entries (Jellyfin user ID → role mapping) |
| `POST` | `/api/pelicula/operators/{id}` | Admin, CSRF-soft | Set or update a user's role (`viewer`, `manager`, `admin`) |
| `DELETE` | `/api/pelicula/operators/{id}` | Admin, CSRF-soft | Remove a role entry |

### User management (Jellyfin)

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/users` | Admin, CSRF-soft | List Jellyfin users (hides internal service account) |
| `POST` | `/api/pelicula/users` | Admin, CSRF-soft | Create Jellyfin user (username + password required) |
| `DELETE` | `/api/pelicula/users/{id}` | Admin, CSRF-soft | Delete user; rejects deletion of the last admin |
| `POST` | `/api/pelicula/users/{id}/password` | Admin, CSRF-soft | Reset a user's Jellyfin password |
| `POST` | `/api/pelicula/users/{id}/disable` | Admin, CSRF-soft | Disable a Jellyfin user account |
| `POST` | `/api/pelicula/users/{id}/enable` | Admin, CSRF-soft | Re-enable a disabled Jellyfin user account |
| `POST` | `/api/pelicula/users/{id}/library` | Admin, CSRF-soft | Set Jellyfin library access (`{"movies": bool, "tv": bool}`) |
| `GET` | `/api/pelicula/sessions` | Viewer+ | Active Jellyfin sessions (now-playing card) |

### Request queue

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/requests` | Viewer+ | List requests. Admins see all; viewers see only their own |
| `POST` | `/api/pelicula/requests` | Viewer+ | Create a media request (`type`, `tmdb_id`/`tvdb_id`, `title`, `year`). Optional `seasons` (int array, series only) is the viewer's desired season-level scope — absent/null means all seasons, `[]` is rejected with 400, and each number is shape-validated (0-999, deduped, ≤100 entries) but **not** checked for existence against Sonarr; that check happens at approve, the authoritative gate. `seasons` on a movie is rejected with 400 |
| `POST` | `/api/pelicula/requests/{id}/approve` | Admin | Approve a request; adds to Radarr/Sonarr and marks available. Optional body `{"seasons": [...]}` (series only) lets the admin override the viewer's requested scope at approval time: **absent/null** uses the request's stored `seasons`; a **non-empty array** is shape-validated and used as the final scope (existence against Sonarr's lookup is validated here — a season number that doesn't exist on the series is rejected with 400); an **explicit `[]`** clears the scope to all seasons. This is intentionally asymmetric with `search/add` and request-create (where `[]` is rejected) — the approval UI has no per-series season list to enumerate against ahead of the Sonarr lookup, so `[]` here means "no override, use everything" rather than "invalid input" |
| `POST` | `/api/pelicula/requests/{id}/deny` | Admin | Deny a pending request |
| `DELETE` | `/api/pelicula/requests/{id}` | Admin | Hard-delete a request record |

### Search and discovery

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/search` | Manager+ | Unified TMDB/TVDB/Prowlarr search. Query: `?q=…&type=movie|series`. Series results carry an additive `seasons` array (`seasonNumber`, plus `episodeCount` when Sonarr's lookup provides `statistics.totalEpisodeCount` — never fabricated when absent) |
| `POST` | `/api/pelicula/search/add` | Manager+ | Add a movie (`tmdbId`) or series (`tvdbId`) to Radarr/Sonarr. Optional `profileId` (int) / `rootPath` (string) override the default quality profile / root folder — absent or zero preserves today's default exactly. `profileId` must match an id from `GET /api/pelicula/arr-meta`'s `qualityProfiles` for that arr; `rootPath` must match a **registered library's container path for that arr**, as returned in arr-meta's `libraries` field (not `rootFolders`, which is the *arr's own root-folder list and may not coincide with registered libraries on custom-library setups). Either mismatch is rejected with 400. Optional `seasons` (int array, series only) selects which seasons to monitor — absent/null means all seasons (the payload sent to Sonarr is unchanged from before season support existed); `[]` is rejected with 400; each number is shape-validated (0-999, deduped, ≤100 entries) and then checked for existence against Sonarr's own lookup, with a 400 on any number that doesn't exist for that series. `seasons` on a movie is rejected with 400 |

### Catalog (Radarr/Sonarr + Procula)

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/catalog` | Viewer+ | List movies and series from Radarr+Sonarr. Optional `?q=…&type=movie|series` filter. Response: `{movies, series, errors?}` — `errors` (e.g. `{"radarr": "unreachable"}`) is present only when a service fetch failed, so an empty list with `errors` set means "unavailable", not "empty library" |
| `GET` | `/api/pelicula/catalog/series/{id}` | Viewer+ | Sonarr series detail by Sonarr internal ID |
| `GET` | `/api/pelicula/catalog/series/{id}/season/{n}` | Viewer+ | Episode + episodefile list for a specific season |
| `GET` | `/api/pelicula/catalog/item/history` | Viewer+ | Procula job history for a file path (`?path=…`) |
| `GET` | `/api/pelicula/catalog/flags` | Viewer+ | Proxies Procula's catalog flags |
| `GET` | `/api/pelicula/catalog/detail` | Viewer+ | Detail for a file path: flags, active job, synopsis, artwork (`?path=…`) |
| `GET` | `/api/pelicula/catalog/items` | Viewer+ | List catalog items with optional `?type=…&tier=…&q=…` filters |
| `GET` | `/api/pelicula/catalog/items/{id}` | Viewer+ | Single catalog item by ID |
| `POST` | `/api/pelicula/catalog/backfill` | Admin | Trigger background backfill from Radarr+Sonarr into the catalog DB. A full resync: ends with the orphan reconciler and the stale-row sweep, so rows whose media left Radarr/Sonarr/Jellyfin are removed |
| `POST` | `/api/pelicula/catalog/reconcile` | Admin | Run the orphan reconciler synchronously and return the result |
| `POST` | `/api/pelicula/catalog/command` | Admin | Proxy force-search, rescan, or unmonitor to Radarr/Sonarr (`arr_type`, `arr_id`, `command`) |
| `POST` | `/api/pelicula/catalog/replace` | Admin | Mark release as failed in *arr, rescan, and re-search (`arr_type`, `arr_id`, `episode_id`, `path`) |
| `DELETE` | `/api/pelicula/catalog/blocklist/{id}` | Admin | Remove an entry from the *arr blocklist (`?arr_type=radarr|sonarr`). 204 on success, 502 if the *arr delete fails |
| `GET` | `/api/pelicula/catalog/qualityprofiles` | Viewer+ | Returns `{radarr: {id: name}, sonarr: {id: name}}` quality profile maps |
| `GET` | `/api/pelicula/jobs` | Viewer+ | Procula job list grouped by state (proxied) |

### Libraries

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/libraries` | Public | List registered libraries (path and built-in flag omitted for unauthenticated callers) |
| `POST` | `/api/pelicula/libraries` | Admin, CSRF-strict | Add a new library (slug, name, type, arr, processing) |
| `PUT` | `/api/pelicula/libraries/{slug}` | Admin, CSRF-strict | Update an existing library (slug and built-in flag are immutable) |
| `DELETE` | `/api/pelicula/libraries/{slug}` | Admin, CSRF-strict | Delete a non-built-in library |

### Local import wizard

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/browse` | Admin | Server-side folder browser. Returns directory entries under `/downloads`, `/media`, and `IMPORT_SOURCE_DIR`. Resolves symlinks and re-checks against allowlist to prevent path escape |
| `POST` | `/api/pelicula/library/scan` | Admin, CSRF-strict | Match local media files (or folders) against Radarr/Sonarr. Returns per-file match plan with confidence levels |
| `POST` | `/api/pelicula/library/apply` | Admin, CSRF-strict | Apply matched items — add to *arr. Moves files on disk |
| `GET` | `/api/pelicula/library/suggest-path` | Manager+ | Suggest a library destination path for a title. Query: `?type=movie|series&title=…&year=…&season=…` |

### Transcoding and subtitle re-acquisition

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/transcode/profiles` | Admin | List transcode profiles from Procula |
| `POST` | `/api/pelicula/transcode/profiles` | Admin | Create or update a transcode profile |
| `DELETE` | `/api/pelicula/transcode/profiles/{name}` | Admin | Delete a transcode profile by name |
| `POST` | `/api/pelicula/library/retranscode` | Admin | Enqueue manual transcode jobs for a list of file paths (`files`, `profile`) |
| `POST` | `/api/pelicula/library/resub` | Admin | Trigger Bazarr subtitle search for a file path via Procula (`{"path": "…"}`) |
| `POST` | `/api/pelicula/procula/jobs/{id}/resub` | Admin | Re-trigger subtitle search for a specific Procula job |
| `POST` | `/api/pelicula/procula/jobs/{id}/retry` | Admin | Re-queue a failed Procula job |
| `POST` | `/api/pelicula/procula/jobs/{id}/cancel` | Admin | Cancel an in-progress or queued Procula job |

### Download management

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/downloads` | Viewer+ | Current torrent list with per-torrent state |
| `GET` | `/api/pelicula/downloads/stats` | Viewer+ | Aggregate download/upload speed and active/queued counts |
| `POST` | `/api/pelicula/downloads/pause` | Manager+ | Pause or resume a torrent (`hash`, `paused: bool`). Uses qBittorrent v5 stop/start API |
| `POST` | `/api/pelicula/downloads/cancel` | Admin | Remove torrent + files, remove from *arr queue, optionally blocklist (`hash`, `category`, `blocklist: bool`) |

### Settings

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/settings` | Admin, CSRF-strict | Read runtime config (`.env` values). Sensitive fields (WireGuard key, Procula key) are masked |
| `POST` | `/api/pelicula/settings` | Admin, CSRF-strict | Write runtime config. Requires local Origin header (RFC1918 or localhost) |
| `POST` | `/api/pelicula/settings/reset` | Admin, CSRF-strict | Full settings reset from a new WireGuard key. Same Origin guard |
| `GET` | `/api/pelicula/procula-settings` | Admin | Read Procula settings (proxied) |
| `POST` | `/api/pelicula/procula-settings` | Admin | Write Procula settings (proxied, with API key) |
| `GET` | `/api/pelicula/arr-meta` | Manager+ | Quality profiles and root folders from Radarr + Sonarr, plus a `libraries` field (`{name, path}` per registered library for that arr — the container paths `search/add`'s `rootPath` validates against), for settings dropdowns and the search "Add with options…" modal. Non-sensitive on a LAN-only stack; relaxed from Admin since search/add is already Manager+ |

### Dashboard data

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/health` | Public | Service health check. Returns `{"status":"ok"}` |
| `GET` | `/api/pelicula/status` | Viewer+ | VPN status, service health, wired flag |
| `GET` | `/api/pelicula/host` | Viewer+ | Container uptime, disk usage, library counts (movie + series totals) |
| `GET` | `/api/pelicula/processing` | Viewer+ | Procula status + job queue (merged, for dashboard Processing section) |
| `GET` | `/api/pelicula/journey` | Viewer+ | Per-title journey through the canonical six-stage rail. Two query forms: `?type=movie\|series&tmdb_id=…`/`tvdb_id=…`, or `?arr_type=radarr\|sonarr&arr_id=…` (400 when neither is complete; 404 for an unknown title). Response: `{type, title, year, tmdb_id, tvdb_id, arr_type, arr_id, monitored, has_file, current_stage, progress?, stages, request?, degraded?}` — `stages` is always all six of `requested, approved, searching, downloading, processing, available` in order, each `{stage, status: done\|active\|pending\|skipped}` with optional `at`/`by`/`progress`/`detail`/`eta`. Request-derived fields are scoped server-side: only the request's owner or an admin gets the `request` object and `requested`/`approved` timestamps — other viewers see those two stages as `skipped` with no attribution. Unreachable upstreams degrade instead of failing: still 200, with `degraded` listing them (e.g. `["qbt"]`). Upstream fan-out is bounded by 10s in-memory snapshot caches (*arr queues, qBittorrent torrents, Procula jobs) |
| `GET` | `/api/pelicula/storage` | Viewer+ | Procula storage stats (proxied) |
| `POST` | `/api/pelicula/storage/scan` | Admin | Trigger Procula storage scan (proxied) |
| `GET` | `/api/pelicula/updates` | Viewer+ | Procula update check result (proxied) |
| `GET` | `/api/pelicula/notifications` | Viewer+ | Merged Procula + *arr history feed |
| `GET` | `/api/pelicula/network` | Admin | Per-container bandwidth stats. Response: `{containers: [{name, bytes_in, bytes_out, vpn_routed}…], as_of}`. 10s in-memory cache. VPN-profile containers (`gluetun`, `qbittorrent`, `prowlarr`) are flagged `vpn_routed: true` |
| `POST` | `/api/pelicula/speedtest` | Admin | Run VPN speed test via gluetun HTTP proxy |
| `GET` | `/api/pelicula/logs/aggregate` | Admin | Fan-in log lines from all containers |
| `GET` | `/api/pelicula/sse` | Viewer+ | Server-Sent Events stream for real-time dashboard updates |

### Action bus

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/actions/registry` | Viewer+ | List registered Procula action handlers (60s cache) |
| `POST` | `/api/pelicula/actions` | Admin | Dispatch an action to the Procula action bus (proxied with API key). Optional `?wait=…` |

The registry currently returns six actions: `validate`, `transcode`, `subtitle_search`, `dualsub`, and `replace` (all `applies_to: ["movie","episode"]`, fan out per-episode at series/season level in the dashboard), plus `remove` (`applies_to: ["movie","series"]` — whole-title deletion, rendered as a single non-fanout action at series level). See docs/PROCULA.md's Action Bus section for the full catalog and the `remove` design decision.

### Admin / container control

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `POST` | `/api/pelicula/admin/stack/restart` | Admin | Restart all stack containers in dependency order; restarts `pelicula-api` last (async). Rate-limited (30 r/m, burst=10) at nginx layer |
| `POST` | `/api/pelicula/admin/vpn/restart` | Admin | Restart VPN stack (`gluetun`, `qbittorrent`, `prowlarr`). Rate-limited (30 r/m, burst=10) |
| `GET` | `/api/pelicula/admin/logs` | Admin | Recent log lines for a named container (`?svc=…&tail=…`). Rate-limited (30 r/m, burst=10) |

### Backup and export

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/export` | Admin | Download backup (watchlist + roles + invites + requests) |
| `POST` | `/api/pelicula/export` | Admin | Trigger and return a backup |
| `POST` | `/api/pelicula/import-backup` | Admin | Restore from a backup produced by `GET/POST /api/pelicula/export` |

### Jellyfin integration

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/jellyfin/info` | Public | Jellyfin discovery info: `{web_url, lan_url}`. Used by `/register` and native apps. No API key returned |

### Internal (Docker-network-only)

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `POST` | `/api/pelicula/hooks/import` | Internal | Receives Radarr/Sonarr import webhooks, normalizes payload, forwards to Procula. Validates `X-Webhook-Secret` header against `WEBHOOK_SECRET` env var (check skipped when unset) |
| `POST` | `/api/pelicula/jellyfin/refresh` | Internal | Triggers Jellyfin library scan. Called by Procula; requires `X-API-Key: <PROCULA_API_KEY>` |
| `POST` | `/api/pelicula/catalog/remove` | Internal | Deletes a whole title — files, *arr entry (`DELETE .../movie\|series/{id}?deleteFiles=true`), and catalog rows. Called by Procula's `remove` action handler; requires `X-API-Key: <PROCULA_API_KEY>`. Request: `{"arr_type":"radarr"\|"sonarr","arr_id":N}`. Response: `{"removed":true,"arr_type":…,"arr_id":…,"title":…,"file_paths":[…]}`. Idempotent — a 404 from the *arr delete (already gone) is treated as success |

---

### Rate-limited endpoints (nginx layer)

The following endpoints are rate-limited at the nginx proxy layer (zone `peli_auth`: 10 requests/minute per source IP). Bursts shown are the `nodelay` burst headroom before requests start receiving HTTP 429.

| Endpoint | Burst |
|----------|-------|
| `POST /api/pelicula/auth/login` | 5 |
| `POST /api/pelicula/register` | 3 |
| `GET /api/pelicula/invites/{token}/check` | 5 |
| `POST /api/pelicula/invites/{token}/redeem` | 5 |
| `GET /api/pelicula/generate-password` | 5 |

The `/api/pelicula/admin/*` endpoints use a separate zone (`admin`: 30 r/m, burst=10).

---

## Procula API (port 8282, proxied at /api/procula/)

See PROCULA.md for full Procula endpoint reference and pipeline details.

---

## Backup Format

Backups are versioned JSON files produced by `POST /api/pelicula/export` and consumed by `POST /api/pelicula/import-backup`. The import endpoint accepts any version from 1 to the current version and auto-migrates forward.

| Version | Fields | Notes |
|---------|--------|-------|
| v1 | `version`, `exported`, `movies`, `series` | Original format — watchlist only |
| v2 | v1 + `pelicula_version`, `roles`, `invites`, `requests` | Full data export including auth and request queue state |

**Forward compatibility:** Newer versions always accept older backups. Fields added in later versions get sensible defaults when importing from an older version. The `version` field is always present and always an integer.

**Requests entries (v2, additive):** each entry in `requests` now optionally carries a `seasons` int array — the season-level scope recorded for a series request (absent/omitted means all seasons). This is an additive field on the existing v2 format, not a new backup version; older v2 backups without `seasons` restore with an empty (all-seasons) scope.

---

## Environment Variable Overrides

Service URLs default to Docker-internal addresses. Override these when running services on non-standard ports or external hosts.

### Service URLs (middleware)

| Variable | Default | Used by |
|----------|---------|---------|
| `SONARR_URL` | `http://sonarr:8989/sonarr` | autowire, health checks |
| `RADARR_URL` | `http://radarr:7878/radarr` | autowire, health checks |
| `PROWLARR_URL` | `http://gluetun:9696/prowlarr` | autowire, health checks |
| `BAZARR_URL` | `http://bazarr:6767/bazarr` | autowire, health checks |
| `JELLYFIN_URL` | `http://jellyfin:8096/jellyfin` | auth, user management, sessions |
| `QBITTORRENT_URL` | `http://gluetun:8080` | download management |
| `GLUETUN_CONTROL_URL` | `http://gluetun:8000` | VPN health checks |
| `APPRISE_URL` | `http://apprise:8000/notify` | notifications |
| `PELICULA_API_URL` | `http://pelicula-api:8181` | webhook callback URL wired into *arr apps |

### Host detection (passed by CLI to middleware in setup mode)

| Variable | Default | Purpose |
|----------|---------|---------|
| `HOST_PLATFORM` | `linux` | Platform label for setup wizard |
| `HOST_TZ` | `America/New_York` | Timezone default |
| `HOST_PUID` | `1000` | Default UID for containers |
| `HOST_PGID` | `1000` | Default GID for containers |
| `HOST_CONFIG_DIR` | `./config` | Default config path suggestion |
| `HOST_LIBRARY_DIR` | `~/media` | Default library path suggestion |
| `HOST_WORK_DIR` | `~/media` | Default work path suggestion |
