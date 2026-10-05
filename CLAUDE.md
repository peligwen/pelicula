# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## About Pelicula v1

**Pelicula** is a clone-and-run media stack. The `pelicula` Go CLI handles setup (a terminal wizard), lifecycle and diagnostics for a Docker Compose stack of 8 containers (5 without the VPN) behind an nginx reverse proxy on port **7354** (PELI on a phone keypad). One Go server, `pelicula-server`, auto-wires the stack, serves the dashboard API and validates imports.

This tree is a from-scratch rescope of the previous implementation (`middleware/`, `procula/` and the old `cmd/pelicula/`), which is preserved in git history under the `pre-rescope` tag. The point of v1 is to have less: do not port features back from the old tree without being asked.

## Layout

```
pelicula                 bash wrapper: builds ./cmd/pelicula into bin/pelicula (Docker fallback), execs it
cmd/pelicula/            CLI, stdlib only; must not import pelicula/internal/...
cmd/pelicula-server/     server binary: wires store, clients, autowire, pipeline, portsync, HTTP
internal/config/         env to Config; reads *arr API keys from mounted config.xml
internal/store/          SQLite: roles, sessions, invites, requests, jobs, settings
internal/httpx/          retrying HTTP client with API-key redaction
internal/clients/        arr, qbt, jellyfin, gluetun clients
internal/auth/           sessions, roles, invites, login/register, Guard, CSRF
internal/api/            dashboard routes and the import webhook
internal/pipeline/       job worker: ffprobe validate, pass/fail handling, Jellyfin refresh
internal/autowire/       startup wiring of *arr and Jellyfin (idempotent)
internal/portsync/       gluetun forwarded port to qBittorrent listen port
compose/                 docker-compose.yml (the one file), Dockerfile, generated TUN override
nginx/                   nginx.conf and html/ (dashboard: index.html, app.js, styles.css, register.*)
tests/integration/       go test -tags integration; starts a real stack (CI, needs Docker)
tests/playwright/        3 browser specs selecting by data-testid
docs/                    ARCHITECTURE.md, API.md, ROADMAP.md
.env                     generated on first `pelicula up`; gitignored; only the CLI writes it
```

## CLI Commands

```
pelicula up                  # first run: terminal wizard writes .env; then seed configs, compose up, wait for VPN and /api/health
pelicula down|status|logs [svc]|restart [svc]|update|check-vpn|doctor|version
pelicula reset-config [svc|all]   # asks y/N (--yes skips); removes seeded config dirs, keeps .env
pelicula seed <config_dir>        # write/re-enforce service configs only; no Docker (the e2e test uses it)
pelicula help
```

Global flag `--debug`. Compose project name is `pelicula` (`PELICULA_PROJECT_NAME` overrides). The CLI exports `PELICULA_VPN=true|false` and `PELICULA_VERSION` to compose and passes `--profile vpn` only when `WIREGUARD_PRIVATE_KEY` is set.

Not in v1 on purpose: export, import-backup, import, rebuild, redeploy, restart-acquire, test, verify.

## Architecture

```
nginx (:7354) ─── /                 → static dashboard
               ── /register         → register.html (public)
               ── /api/*            → pelicula server (:8181)
               ── /sonarr /radarr   → Sonarr / Radarr   (auth_request gate)
               ── /prowlarr /qbt/   → gluetun:9696 / :8080 (auth_request gate, vpn profile)
               ── /jellyfin         → Jellyfin (own auth)
```

- **Server startup order:** store open + `ResetRunningJobs`, clients, `WaitForAPIKeys` (background), `autowire.Run` (background, sets `wired`), pipeline worker, portsync (VPN only), HTTP wrapped in `auth.CSRF`.
- **Source of truth:** Sonarr/Radarr own the catalog, qBittorrent owns downloads, Jellyfin owns users and passwords, the CLI owns `.env` and seeded XML/INI. SQLite holds only roles, sessions, invites, requests, jobs and three runtime settings.
- **Auth:** Jellyfin `AuthenticateByName`; role from the `roles` table (default admin for Jellyfin admins, else viewer); `pelicula_session` cookie; single-use invites; `auth_request` gate for the *arr UIs; nginx rate-limits login and register.
- **Pipeline:** *arr webhook (`X-Webhook-Secret`) enqueues a job, ffprobe validates; pass marks requests available and refreshes Jellyfin; fail marks history failed in *arr, deletes only the matching file via the *arr API, and re-searches.
- **qBittorrent and Prowlarr share gluetun's network namespace** and are reached at `gluetun:8080` and `gluetun:9696`, never by their own container names.

Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), [docs/API.md](docs/API.md), [docs/ROADMAP.md](docs/ROADMAP.md).

## Key Constraints

- **Gluetun** is pinned to `v3.41.0`; `latest` tracks an unstable dev branch.
- **ProtonVPN requires a paid plan** (Plus or higher); the free tier lacks P2P and port forwarding.
- **Do NOT enable "Moderate NAT"** when generating the WireGuard key; it breaks port forwarding.
- **LinuxServer.io images are Alpine-based**: healthchecks use `wget`, not `curl`.
- **`modernc.org/sqlite` is the single external dependency** (pure Go, no CGO). Do not add another. The CLI is stdlib-only.
- **qBittorrent v5** renamed pause/resume to stop/start; the client uses `/torrents/stop` and `/torrents/start`.
- **All paths are env vars** (`CONFIG_DIR`, `LIBRARY_DIR`, `WORK_DIR`, `MOVIES_PATH`, `TV_PATH`). Never hardcode a host path in compose or Go.
- **The server mounts `/media` read-only.** It never writes or deletes media; file removal goes through the *arr API.
- **The CLI is the only writer of `.env`** and of the seeded `config.xml`, `qBittorrent.conf`, `categories.json` and `network.xml`. The server only reads its environment.
- **No inline scripts in the frontend** (CSP is `script-src 'self'`); no CDNs, no external fonts.
- **LAN only.** Port 7354 is HTTP and not meant for the internet. Jellyfin's own HTTPS on 8920 is the only externally exposed surface.
- Logging is `log/slog`; pass `context.Context` to everything that does I/O.

## Testing

```
make test        # go test -race ./...            (no Docker needed)
make vet         # go vet ./... and a gofmt check
make lint        # staticcheck via go run
make e2e         # go test -tags integration ./tests/integration/...   (needs Docker; port 7399, project pelicula-test, no VPN)
make playwright  # cd tests/playwright && npm test   (needs a running stack; PELICULA_URL, PELICULA_ADMIN_USER, PELICULA_ADMIN_PASSWORD)
make build       # bin/pelicula and bin/pelicula-server
```

Every package has unit tests using `httptest` fakes and `store.OpenMemory()`. Nothing in `go test ./...` may require a running stack. Run `gofmt -l .`, `go vet ./...` and `go test ./...` from the repository root before committing.

## Scope discipline

- **No second source of truth.** If Sonarr, Radarr, qBittorrent, Jellyfin or `.env` already owns a fact, read it from there. Do not cache it in SQLite or write a mirror.
- **New features go behind a compose profile or a flag**, off by default. Anything on the deferred list in docs/ROADMAP.md (Bazarr, transcoding, backups, Apprise, hardware acceleration, NFS, import wizard, SSE, open registration, dual subtitles) must not return outside that shape.
- **Delete something each round.** When a change adds code, look for code, a setting, a test or a doc paragraph that the change makes unnecessary and remove it in the same round.
- Keep the dashboard small: `app.js` stays around 900 lines; split a helper rather than grow it.

## Bug Fixing

- Before editing, confirm which side of the stack (frontend, backend or infra) should own the change. When a fix could go either way (JSON key mismatches such as `type` versus `media_type`, data format differences), ask rather than assume.

## Debugging

- For infrastructure and networking issues (Docker, VPN routing, API connectivity), check network topology and container routing first, before diving into code. Connectivity failures in this stack are more often config or infra than code bugs. `pelicula doctor` is the first stop.

## Refactoring

- When replacing a pattern across files, exhaustively grep with multiple search terms (different indentation, aliasing, method chaining) to catalog every instance before editing.

## Git & Commits

- Commit changes in logical chunks as you go; do not accumulate large uncommitted diffs.
- Group related changes into focused commits; never bundle unrelated changes.
