# API Reference

All `pelicula-api` endpoints are proxied through nginx at `/api/pelicula/`. Internal endpoints are restricted to Docker networks in nginx config — not reachable from the LAN.

All mutating endpoints require an admin or manager session. A session is either cookie-based (from `POST /api/pelicula/login`) or the loopback auto-session granted to requests from the host machine — see [docs/PELIGROSA.md](PELIGROSA.md#loopback-auto-session).

Auth levels: **Admin** = session with admin role; **Manager+** = manager or admin session; **Viewer+** = any authenticated session; **Public** = no auth required; **Internal** = Docker-network-only.

## Stability Policy

**Stable since v1.0.** All endpoints below are part of the public API contract:

- **Fields are additive only** — response fields are never removed or renamed
- **New endpoints may be added** in minor releases
- **Breaking changes** (field removal, type changes, endpoint removal) only at major version bumps
- **Frontend treats unknown fields as ignorable** — new fields won't break older dashboard versions

## Download Management

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/downloads` | Viewer+ | Current torrent list |
| `POST` | `/api/pelicula/downloads/pause` | Admin | Pause/resume via qBittorrent stop/start API (v5+) |
| `POST` | `/api/pelicula/downloads/cancel` | Admin | Remove torrent + files, remove from *arr queue, unmonitor item. Optional `blocklist: true` |

## Settings and Setup

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/settings` | Admin | Read runtime config (`.env` values) |
| `POST` | `/api/pelicula/settings` | Admin | Write runtime config. Requires local Origin header (RFC1918 or localhost) — empty Origin rejected |
| `POST` | `/api/pelicula/settings/reset` | Admin | Full settings reset from a new WireGuard key. Same Origin guard |
| `POST` | `/api/pelicula/setup` | Public | Browser setup wizard: validate inputs, generate `.env`, create directories. Only available when `SETUP_MODE=true` |
| `GET` | `/api/pelicula/browse` | Admin | Server-side folder browser for import wizard. Resolves symlinks and re-checks against allowlist to prevent path escape |
| `GET` | `/api/pelicula/library/scan` | Admin | Match local media files against Radarr/Sonarr |
| `POST` | `/api/pelicula/library/apply` | Admin | Apply matched items (add to *arr) |

## Auth, Users, and Invites

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/users` | Admin | List Jellyfin users |
| `POST` | `/api/pelicula/users` | Admin | Create user |
| `DELETE` | `/api/pelicula/users/:id` | Admin | Delete user |
| `POST` | `/api/pelicula/users/:id` | Admin | Reset password |
| `GET` | `/api/pelicula/invites` | Admin | List active invite links |
| `POST` | `/api/pelicula/invites` | Admin | Create invite link |
| `GET` | `/api/pelicula/invites/:token` | Public | Check invite validity |
| `POST` | `/api/pelicula/invites/:token/redeem` | Public (invite gated) | Self-service viewer registration |
| `GET` | `/api/pelicula/sessions` | Admin | Active Jellyfin sessions (now-playing card) |

## Dashboard Data

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `GET` | `/api/pelicula/status` | Viewer+ | VPN status, service health, wired flag |
| `GET` | `/api/pelicula/notifications` | Viewer+ | Merged Procula + *arr history feed |
| `GET` | `/api/pelicula/storage` | Viewer+ | Proxies Procula storage stats |
| `GET` | `/api/pelicula/updates` | Viewer+ | Proxies Procula update check |
| `GET` | `/api/pelicula/processing` | Viewer+ | Proxies Procula job status + queue for dashboard |
| `GET` | `/api/pelicula/export` | Admin | Export watchlist/backup |
| `POST` | `/api/pelicula/export` | Admin | Trigger export |
| `POST` | `/api/pelicula/import-backup` | Admin | Restore from backup |

## Procula Integration (Internal)

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| `POST` | `/api/pelicula/hooks/import` | Internal | Receives Radarr/Sonarr import webhooks, normalizes payload, forwards to Procula. Auto-wired by `wireImportWebhook()`. Validates `WEBHOOK_SECRET` query param when set in `.env` |
| `POST` | `/api/pelicula/jellyfin/refresh` | Internal | Triggers Jellyfin library scan. Called by Procula; requires `X-API-Key: <PROCULA_API_KEY>` |

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
