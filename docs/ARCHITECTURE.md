# Architecture

Pelicula v1 is a Docker Compose stack of eight containers (five without the VPN) behind one nginx port, a bash wrapper and Go CLI that own setup and lifecycle, and one Go server that auto-wires the stack, serves the dashboard API and validates imports. The guiding rule is **one source of truth per fact**: Sonarr and Radarr own the catalog, qBittorrent owns downloads, Jellyfin owns users, and the CLI owns configuration. The server stores only what none of them can.

## Overview

```
                          http://localhost:7354
                                   |
                         nginx (nginx:1.28.3-alpine)
                                   |
  /                  static dashboard (index.html, app.js, styles.css)
  /register          register.html (public)
  /api/auth/login    rate limited --+
  /api/register[/..] rate limited --+--> pelicula :8181
  /api/hooks/import  LAN IPs only  --+        |
  /api/*             ----------------+        |  reads/writes via HTTP
                                              +--> sonarr   :8989 /sonarr
  /sonarr  --- auth_request --> sonarr        +--> radarr   :7878 /radarr
  /radarr  --- auth_request --> radarr        +--> jellyfin :8096 /jellyfin
  /prowlarr -- auth_request --> gluetun:9696  +--> gluetun  :8000 (control API)  [vpn]
  /qbt/ ------ auth_request --> gluetun:8080  +--> gluetun  :9696 prowlarr       [vpn]
  /jellyfin  (no gate) -------> jellyfin      +--> gluetun  :8080 qbittorrent    [vpn]

  gluetun (WireGuard tunnel) owns the network namespace of qbittorrent and prowlarr  [vpn profile]
  jellyfin also publishes 8920 (HTTPS) and 7359/udp (discovery) directly on the host
```

Containers: `nginx`, `pelicula`, `sonarr`, `radarr`, `jellyfin`, and with the `vpn` profile `gluetun`, `qbittorrent`, `prowlarr`. qBittorrent and Prowlarr share gluetun's network namespace, so they are reached as `gluetun:8080` and `gluetun:9696`, never by their own container names. If the tunnel drops they lose connectivity (kill switch).

## Processes

There is one Go module (`pelicula`, Go 1.25) and two binaries.

| Binary | Runs | Does | May import `internal/...` |
|---|---|---|---|
| `cmd/pelicula` | on the host | setup wizard, seeding configs, `docker compose` lifecycle, diagnostics | no (stdlib only) |
| `cmd/pelicula-server` | in the `pelicula` container | HTTP API, auth, auto-wiring, import pipeline, port sync | yes |

The CLI and the server never talk to each other directly. They meet at `.env` and the compose file: the CLI writes `.env`, compose turns it into the container environment, and the server reads that environment. The only external Go dependency is `modernc.org/sqlite` (pure Go, no CGO), used by the server.

Server packages:

| Package | Role |
|---|---|
| `internal/config` | environment to `Config`; reads *arr API keys from mounted `config.xml` |
| `internal/store` | the SQLite database (see Data model) |
| `internal/httpx` | retrying HTTP client with API-key redaction |
| `internal/clients/{arr,qbt,jellyfin,gluetun}` | thin clients for each upstream |
| `internal/auth` | sessions, roles, invites, login and register, `Guard`, CSRF |
| `internal/api` | dashboard routes and the import webhook |
| `internal/pipeline` | job worker: ffprobe validation and failure handling |
| `internal/autowire` | idempotent startup wiring of *arr and Jellyfin |
| `internal/portsync` | gluetun forwarded port to qBittorrent listen port |

## Server startup sequence

`cmd/pelicula-server` does the following, in order:

1. **Store.** `config.FromEnv`, `store.Open(DBPath)` (creating `CONFIG_DIR/pelicula/` and migrating), then `store.ResetRunningJobs` so jobs interrupted by a crash or restart go back to `queued`.
2. **Clients.** Build the Sonarr, Radarr and Jellyfin clients and the Jellyfin `Admin` token cache. When `PELICULA_VPN=true`, also Prowlarr, qBittorrent and gluetun. Prowlarr, QBT and Gluetun are `nil` otherwise, and every feature that touches them degrades (downloads list is empty, status shows `vpn.enabled: false`).
3. **`WaitForAPIKeys`**, in the background, bounded to 5 minutes: poll `CONFIG_DIR/<svc>/config.xml` every 3 s until Sonarr, Radarr (and Prowlarr) have written their API keys, then `SetAPIKey` on each client.
4. **`autowire.Run`**, in the background, after the keys exist. It waits for every service to answer ping, completes the Jellyfin wizard and libraries, ensures root folders, the qBittorrent download client, the import webhook and the Prowlarr applications. When it finishes it sets the `wired` flag that `/api/health` and `/api/status` report. Each step logs and continues on failure; the first error is logged and the server still serves.
5. **Pipeline worker** starts (`Worker.Run`): claims queued jobs one at a time. `Kick` is called after every enqueue.
6. **Port sync** starts when the VPN is enabled: every 60 s it reads gluetun's forwarded port and sets qBittorrent's listen port when they differ.
7. **HTTP** serves `auth.Routes` and `api.Routes` on one `http.ServeMux`, wrapped in `auth.CSRF`, and shuts down gracefully on SIGTERM (`stop_grace_period: 20s`).

Steps 3 and 4 run in goroutines and the server does not wait for them, so `/api/health` answers (with `wired: false`) while wiring is still in progress. The CLI and the integration test both wait on `wired: true`.

## Data model

One SQLite file, `CONFIG_DIR/pelicula/pelicula.db`, six tables, schema version 1 (`PRAGMA user_version`). One connection, WAL mode, foreign keys on. Defined in `internal/store/store.go`.

| Table | Holds |
|---|---|
| `roles` | `username` to `role` (viewer, manager, admin) and `updated_at`. The only authorization state Pelicula adds on top of Jellyfin. |
| `sessions` | Cookie sessions: random `token`, `username`, `role`, the Jellyfin user id and Jellyfin token from login, `expires_at`, `created_at`. `SetRole` updates live sessions; `DeleteRole` removes them. |
| `invites` | One-time registration codes: `code`, granted `role`, `created_by`, `created_at`, `expires_at`, `used_by`, `used_at`. |
| `requests` | What viewers asked for: media type, TMDB/TVDB ids, title, year, poster, `requested_by`, `status` (pending, approved, declined, available), `arr_id`, `decided_by`, `note`, timestamps. |
| `jobs` | One row per imported file sent through the pipeline: *arr type and ids, `path`, `size`, `download_id`, `runtime_min`, `status` (queued, running, passed, failed), `result` (JSON text), `error`, `attempts`, timestamps. `EnqueueJob` dedupes on `path` while a job for it is queued or running. |
| `settings` | Key/value runtime toggles: `validation_enabled`, `auto_blocklist`, `auto_approve_requests`. Defaults live in `store.SettingDefaults`; unset keys read as the default. |

Deliberately **not stored**:

- **Catalog.** Titles, files and quality live in Sonarr and Radarr; search and the "in library" flag read through to them on every request. There is no mirror to reconcile.
- **Downloads.** Live from qBittorrent (`/api/downloads`), joined to the *arr queue only when removing.
- **Users.** Accounts and passwords live in Jellyfin. `GET /api/users` is Jellyfin's user list joined with the `roles` table.
- **Settings that need a restart** (paths, ports, VPN key, secrets). Those live in `.env`, owned by the CLI.

## Auth model

**Jellyfin is the identity provider.** Login posts the username and password to Jellyfin's `AuthenticateByName`. The role is the stored one if there is a `roles` row; otherwise `admin` if Jellyfin says the user is an administrator, else `viewer`, and that default is persisted. A bad password is a 401; an unreachable Jellyfin is a 503.

**Sessions.** A 32-byte random token (hex) in the `pelicula_session` cookie: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Max-Age` equal to the TTL (30 days). The server looks the token up in `sessions` on every guarded request. Logging out deletes the row.

**Roles** rank viewer < manager < admin. `auth.Guard(min, handler)` returns 401 without a session and 403 below the minimum, and puts the `*store.Session` in the request context. Route minimums are in [API.md](API.md).

**Invites.** Admins create single-use codes (16 random bytes, base64url; default 72 h, max 720 h). `POST /api/register` validates the code, creates the Jellyfin user with the admin token, atomically marks the invite used, stores the role, and signs the new user in. A used, expired or unknown code is a 410. There is no open registration.

**nginx layer.** `/api/auth/login`, `/api/register` and the invite lookup `/api/register/{code}` share one rate-limit budget of 10 requests per minute per IP (burst 5, 3 and 5 respectively; excess requests get 429) with an 8 KB body cap; the server does not rate limit itself. `/api/hooks/import` accepts only loopback and private ranges (127.0.0.1, 10/8, 172.16/12, 192.168/16) and additionally needs the `X-Webhook-Secret` header, compared in constant time. The webhook returns 503 if no secret is configured.

**CSRF.** `auth.CSRF` wraps the whole mux. For POST, PUT, PATCH and DELETE, if an `Origin` header (else `Referer`) is present its host must equal the request host (`X-Forwarded-Host` wins when set), otherwise 403. Requests with neither header pass, which is how the *arr webhook, curl and the integration test call the API. Combined with `SameSite=Lax` cookies this stops cross-site form posts.

**Gating the *arr UIs.** Sonarr, Radarr, Prowlarr and qBittorrent run with external authentication (the CLI seeds `AuthenticationMethod External` and a subnet whitelist), so nginx is their only gate. Each location uses `auth_request` against `/api/auth/check`, which returns 204 for any valid session and 401 otherwise; 401 redirects to `/?login=1`. Jellyfin is not gated by nginx because it has its own login.

**Threat model.** Pelicula assumes a trusted LAN. Port 7354 is HTTP only and must not be exposed; the operator forwards Jellyfin's HTTPS port 8920 if they want remote streaming, and Jellyfin's own auth protects it. The server holds the *arr API keys (read-only mount), a Jellyfin admin token and the webhook secret, but never the Docker socket (there is no docker-proxy in v1) and never write access to media.

Known limitations:

- The `auth_request` gate checks for a session, not a role: any signed-in user, including a viewer, can open the *arr and qBittorrent UIs. Treat the people you invite as trusted, or tighten `/api/auth/check` to admin.
- There is no brute-force protection beyond nginx's per-IP rate limit.

## Pipeline

Importing is event driven; nothing polls Sonarr or Radarr.

1. Sonarr or Radarr finishes an import and calls the **webhook** `POST /api/hooks/import` (configured by autowire, with the secret header). `Test` events return ok; only `Download` enqueues a job; everything else is ignored.
2. The handler enqueues a `jobs` row (`queued`) with the file path, size, download id and a best-effort runtime from `GetMovie` / `GetSeriesByID`, then kicks the worker.
3. The worker claims the job and, if `validation_enabled` is false, passes it with `skipped: true`. Otherwise it **validates** the file with ffprobe: it must exist, contain a video stream, not look like a sample (under 50 MB, or under 3 MB per expected minute), and have a duration within 50% of the expected runtime (a warning past 10%; skipped when the runtime is unknown).
4. **Pass.** The job is finished as `passed`, any approved request for that movie or series becomes `available`, and a Jellyfin library refresh is scheduled with a 15 s debounce so a burst of imports triggers one refresh.
5. **Fail.** The job is finished as `failed` with a reason. If `auto_blocklist` is on, the worker marks the matching grab as failed in history through the *arr API (which blocklists the release), deletes **only the file whose path equals the job's path** through the *arr API, and triggers a new search. Every sub-step logs and continues on error.

The server mounts `/media` read-only, so it cannot delete files itself; removal is always delegated to Sonarr or Radarr. Failed jobs can be retried from the dashboard (`POST /api/jobs/{id}/retry`).

## Config ownership

| Owner | Writes | Reads |
|---|---|---|
| CLI (`cmd/pelicula`) | `.env` (setup wizard); seeded `config.xml` for Sonarr, Radarr, Prowlarr; `qBittorrent.conf` and `categories.json`; Jellyfin `network.xml`; `compose/docker-compose.override.yml` (TUN device, Linux only) | `.env` |
| Compose | injects `.env` values into container environments | `.env` and process env |
| Server (`cmd/pelicula-server`) | `CONFIG_DIR/pelicula/pelicula.db` only | its environment; `CONFIG_DIR/{sonarr,radarr,prowlarr}/config.xml` (read-only mount) for API keys |
| *arr and Jellyfin | their own state, including the settings autowire pushes into them | |

Rules that follow from this:

- The CLI is the **only writer of `.env`** and of the seeded XML/INI files. The *arr auth settings are re-enforced on every `up`, so a service cannot lock you out.
- The server reads configuration from the environment only. Anything that needs a container restart belongs in `.env`.
- Dashboard settings are runtime toggles stored in SQLite and take effect immediately.
- Autowire pushes wiring *into* Sonarr, Radarr, Prowlarr and Jellyfin through their APIs; it is idempotent and safe to re-run.

## Compose

One file, `compose/docker-compose.yml`, parameterized by `.env`. No overlays, apart from a generated one for the TUN device.

- **Profiles.** `gluetun`, `qbittorrent` and `prowlarr` carry the `vpn` profile. The CLI passes `--profile vpn` and exports `PELICULA_VPN=true` when `WIREGUARD_PRIVATE_KEY` is set, otherwise `PELICULA_VPN=false` and those services are not created. `PELICULA_VPN` is what the server reads to decide whether to build the VPN clients.
- **TUN override.** On Linux, `pelicula up` checks `/dev/net/tun` and writes `compose/docker-compose.override.yml` mapping it into gluetun. The file is generated and gitignored.
- **Mounts.** `${LIBRARY_DIR}:/media` is read-write in Sonarr, Radarr and Jellyfin and **read-only** in `pelicula`. `${WORK_DIR}/downloads:/downloads` is mounted in Sonarr, Radarr and qBittorrent. `pelicula` mounts `${CONFIG_DIR}/pelicula` read-write and `${CONFIG_DIR}/{sonarr,radarr,prowlarr}` read-only. Every path is an env var; nothing is hardcoded.
- **Images.** gluetun is pinned to `qmcgaw/gluetun:v3.41.0` (`latest` tracks an unstable branch). The `pelicula` image is built from `compose/Dockerfile` (golang:1.25-alpine build, alpine:3.20 with ffmpeg at runtime) and runs as `${PUID}:${PGID}`.
- **Healthchecks** use `wget` because the LinuxServer.io images are Alpine-based and have no `curl`.
- **No** `container_name`, no `networks:` block, no docker-proxy, procula, bazarr or apprise.

## nginx route map

| Location | Target | Notes |
|---|---|---|
| `/`, static files | `/usr/share/nginx/html` | no-cache headers |
| `/register` | `register.html` | public |
| `/api/auth/login` | pelicula :8181 | rate limited 10 r/m per IP (one zone shared with the two rows below), burst 5, body at most 8 KB |
| `/api/register` | pelicula :8181 | rate limited 10 r/m per IP, burst 3, body at most 8 KB |
| `/api/register/{code}` | pelicula :8181 | invite lookup; shares the login/register budget, burst 5; 429 when exceeded |
| `/api/hooks/import` | pelicula :8181 | allow 127.0.0.1, 10/8, 172.16/12, 192.168/16, deny all; body at most 1 MB |
| `/api/` | pelicula :8181 | everything else in [API.md](API.md) |
| `/auth-check` | pelicula `/api/auth/check` | internal; used by `auth_request` |
| `/sonarr`, `/radarr` | sonarr, radarr | `auth_request`; 401 redirects to `/?login=1`; websocket headers |
| `/prowlarr`, `/qbt/` | `gluetun:9696`, `gluetun:8080` | same gate; the upstream is set through a variable plus `resolver 127.0.0.11` so nginx starts with the vpn profile off; `/qbt/` strips its prefix |
| `/jellyfin` | jellyfin :8096 | no gate; long timeouts; buffering off |

Responses carry security headers and, for the dashboard and API, a CSP of `default-src 'self'; img-src 'self' data: https:; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'` (plus `frame-ancestors`, `base-uri` and `form-action` set to `'self'`). The proxied third-party UIs (*arr, qBittorrent, Jellyfin) ship their own CSP and are exempt. The frontend therefore uses no inline scripts, CDNs or external fonts.

## Testing layers

1. **Go unit tests** (`go test -race ./...`). Every package has them. Upstreams are `httptest` fakes, the store is `store.OpenMemory()`, and the pipeline uses a fake ffprobe shell script. Nothing needs Docker.
2. **Go integration test** (`go test -tags integration ./tests/integration/...`, `make e2e`). Starts the real compose stack without the VPN profile in project `pelicula-test` on port 7399 with temporary folders, waits for `wired: true`, and walks login, invite, register, search, request, approve and a fake Radarr webhook through to a job row. It always runs `docker compose down -v`.
3. **Playwright specs** (`make playwright`, in `tests/playwright`). Three browser specs against a running stack: `login`, `invite-register`, `request-approve`. They select elements by `data-testid`.

CI (`.github/workflows/test.yml`) runs gofmt, `go vet`, staticcheck and `go test -race -cover` on the module, shellcheck on the `pelicula` wrapper, and the integration test.
