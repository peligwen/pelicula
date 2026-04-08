# API Reference

All `pelicula-api` endpoints are proxied through nginx at `/api/pelicula/`. Internal endpoints are restricted to Docker networks in nginx config — not reachable from the LAN.

Auth levels: **Admin** = session with admin role (or auth off); **Viewer+** = any authenticated session; **Public** = no auth required; **Internal** = Docker-network-only.

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
