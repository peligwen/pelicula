# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## About Pelicula

**Pelicula** is a clone-and-run media stack. The `pelicula` Go CLI handles setup, lifecycle, and health checks for a Docker Compose stack of 9 services behind an nginx reverse proxy on port **7354** (PELI on a phone keypad).

## Key Files

- `pelicula` — thin bash wrapper at repo root; auto-builds the Go CLI on first run
- `cmd/pelicula/` — Go CLI source (cross-platform: macOS/Linux/Windows/Synology), stdlib-only
- `tests/e2e.sh` — end-to-end integration test runner (bash, standalone)
- `docker-compose.yml` — parameterized with `${CONFIG_DIR}` and `${MEDIA_DIR}` env vars
- `middleware/` — Go backend (pelicula-api): auto-wiring, unified search, download management, auth, settings, request queue. SQLite for mutable state (`modernc.org/sqlite`).
- `procula/` — Go processing pipeline: validation, FFprobe/FFmpeg, transcoding, Jellyfin catalog, storage monitoring. SQLite for job queue and settings.
- `nginx/nginx.conf` — reverse proxy config, path-based routing to all services
- `.env` — generated on first `pelicula up`, gitignored

## CLI Commands

```
pelicula up                  # start stack (runs setup wizard on first run), seed configs, wait for VPN
pelicula down|status|logs [svc]|update|check-vpn
pelicula restart [svc]       # restart service(s) without taking the whole stack down
pelicula rebuild             # rebuild and restart middleware/procula containers
pelicula redeploy [svc]      # rebuild images then full stack down/up (pelicula-api|middleware|procula)
pelicula reset-config        # soft reset: wipe service configs, preserve API keys/VPN/certs/auth
pelicula reset-config [svc]  # per-service reset: sonarr|radarr|prowlarr|jellyfin|qbittorrent|procula-jobs
pelicula reset-config all    # hard reset: wipe config dir + regenerate .env (keeps Prowlarr indexers, VPN key, paths)
pelicula export [file]       # export watchlist / library backup
pelicula import-backup file  # restore from a backup exported by pelicula export
pelicula import [dir]        # import local media files via the browser wizard
pelicula test                # run e2e integration test (isolated stack on port 7399)
```

## Architecture

Nine Docker containers via Docker Compose (plus one opt-in profile service: Apprise):

```
nginx (:7354) ─── /                → dashboard (static HTML)
               ── /api/pelicula/   → pelicula-api (Go middleware, :8181)
               ── /api/procula/    → procula (media processing pipeline, :8282)
               ── /api/vpn/        → gluetun control API
               ── /sonarr/         → Sonarr
               ── /radarr/         → Radarr
               ── /prowlarr/       → Prowlarr (via gluetun network)
               ── /qbt/            → qBittorrent (via gluetun network)
               ── /jellyfin/       → Jellyfin (NOT behind VPN)
```

**pelicula-api** auto-wires the *arr stack on startup and serves the dashboard API. **procula** handles post-import processing (validate → transcode → catalog). **qBittorrent and Prowlarr run on gluetun's network namespace** — reachable at `gluetun:8080` and `gluetun:9696` respectively, not their own container names.

Remote Jellyfin access (Peligrosa) is opt-in — see PELIGROSA.md.

## Key Constraints

- **Gluetun** is pinned to `v3.41.0` — the `latest` tag tracks an unstable dev branch
- **ProtonVPN requires a paid plan** (Plus or higher) — free tier lacks P2P and port forwarding
- **Do NOT enable "Moderate NAT"** when generating the WireGuard key — incompatible with port forwarding
- **Self-signed HTTPS breaks Chrome** — Chrome blocks JS on self-signed cert pages. Default to HTTP for LAN.
- **LinuxServer.io images are Alpine-based** — healthchecks use `wget`, not `curl`
- **Both Go services use `modernc.org/sqlite`** — the single external dependency (pure-Go SQLite driver, no CGO). The Go CLI (`cmd/pelicula/`) is stdlib-only.
- **qBittorrent v5** renamed pause/resume to stop/start — middleware uses the v5 endpoints
- All volume paths in `docker-compose.yml` are env vars — never hardcode paths

## Bug Fixing

- Before editing, confirm which side of the stack (frontend/backend/infra) should own the change. When a fix could go either way (e.g., JSON key mismatches, data format differences), ask rather than assume.

## Debugging

- For infrastructure/networking issues (Docker, VPN routing, API connectivity), check network topology and container routing first — before diving into code. Connectivity failures in this stack are more often config/infra than code bugs.

## Refactoring

- When replacing a pattern across files, exhaustively grep with multiple search terms (different indentation, aliasing, method chaining) to catalog every instance before editing.

## Git & Commits

- Commit changes in logical chunks as you go — don't accumulate large uncommitted diffs.
- Group related changes into focused commits; never bundle unrelated changes.

## More Detail In

- [ARCHITECTURE.md](ARCHITECTURE.md) — compose overlays, middleware startup/auto-wiring, config seeding + `enforce_arr_auth()`, platform detection, nginx file map
- [API.md](API.md) — full `/api/pelicula/*` endpoint catalog with auth levels
- [PELIGROSA.md](PELIGROSA.md) — user interaction safety layer: threat model, auth, users, invites, request queue, webhook secret, CSRF, remote vhost hardening, known limitations
- [PROCULA.md](PROCULA.md) — processing pipeline internals (queue, validate, transcode, catalog, storage)
- [SECURITY.md](SECURITY.md) — vulnerability disclosure policy (threat model and known limitations are in PELIGROSA.md)
- [ROADMAP.md](ROADMAP.md) — active work (Bazarr, invite flow, Go CLI) and deferred items
