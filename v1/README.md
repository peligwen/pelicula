# Pelicula

One command to set up, one command to run. Search for movies and TV shows by name, request or add them, and stream them with Jellyfin. Pelicula wires Sonarr, Radarr, Prowlarr, qBittorrent (behind a ProtonVPN/WireGuard tunnel) and Jellyfin together behind one nginx port and adds a small dashboard on top.

Use it for legal content only. Pelicula does not ship, suggest or configure indexers; there are plenty that carry only legal material.

**LAN only.** Pelicula listens on port 7354 and is meant for a trusted local network. Do not port-forward 7354. Jellyfin's own HTTPS listener on port 8920 is the only surface intended to be reachable from outside; forwarding it is up to you. The threat model is in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#auth-model).

## Quick Start

```bash
git clone https://github.com/peligwen/pelicula.git
cd pelicula          # or cd pelicula/v1 while both trees share a checkout
./pelicula up
```

On the first run `./pelicula up` builds the CLI and asks a few questions in the terminal (folders, an optional WireGuard key, VPN server country). It then generates the secrets, writes `.env`, prints the Jellyfin admin password once, and starts the stack.

Open <http://localhost:7354> and sign in with the Jellyfin admin credentials the wizard printed. They are also stored in `.env` as `JELLYFIN_ADMIN_USER` and `JELLYFIN_PASSWORD`.

## Prerequisites

- **Docker** with the Compose v2 plugin (`docker compose`).
- **Go 1.25** (optional). The `pelicula` wrapper builds the CLI with the local Go toolchain, or inside a `golang:1.25-alpine` container if Go is missing.
- **ProtonVPN Plus or higher** with a WireGuard private key, if you want the VPN. The free tier has no P2P or port forwarding. Do **not** enable "Moderate NAT" when generating the key; it breaks port forwarding. Leave the key blank in the wizard to run without a VPN (see below).
- **bash**. macOS, Linux, WSL and Synology are detected and get sensible default paths.

## What happens on `pelicula up`

1. Creates the config, library and download folders and seeds service configs (URL bases, external auth for the *arr apps, qBittorrent settings, Jellyfin proxy settings). The *arr auth settings are re-applied on every `up`.
2. Starts 8 containers behind nginx (5 without the VPN: Sonarr, Radarr, Jellyfin, pelicula, nginx; the VPN adds gluetun, qBittorrent and Prowlarr).
3. Waits for the VPN tunnel to come up.
4. The `pelicula` server auto-wires everything in the background:
   - qBittorrent as the download client in Sonarr and Radarr;
   - Sonarr and Radarr as applications in Prowlarr;
   - an import webhook in Sonarr and Radarr that calls the server with a shared secret;
   - the Jellyfin startup wizard, an admin user, and the Movies and TV Shows libraries.
5. Waits for `/api/health` to report `wired: true`, then prints the dashboard and Jellyfin URLs.

The only manual step is **adding indexers in Prowlarr** (open it from the dashboard link or at `/prowlarr/` after signing in). Without indexers nothing gets downloaded.

Without a WireGuard key the stack runs with no VPN: Prowlarr and qBittorrent are not started, so there are no downloads, but search, requests, Jellyfin and the import pipeline all work. This is also what the integration test runs.

## Dashboard

The dashboard at <http://localhost:7354/> has four tabs:

- **Search** searches Radarr and Sonarr in parallel and shows interleaved results. Viewers get a *Request* button; managers and admins get *Add*. Titles already in the library show *In library*, and *Watch* once a file exists.
- **Requests** lists requests with a status of pending, approved, declined or available. Viewers see their own; managers and admins see everyone's and can approve or decline.
- **Jobs** shows active downloads (progress, speed, ETA; pause and resume for managers, remove and blocklist for admins) and recent import validations with a Retry button for failed ones.
- **Settings** (admin only) holds three toggles (validation, auto-blocklist, auto-approve requests), a read-only info block, user roles, and invites.

Roles:

| Role | Can do |
|---|---|
| viewer | search, request, see own requests, see jobs and downloads |
| manager | plus add titles directly, approve and decline requests, pause and resume downloads, retry jobs |
| admin | plus settings, invites, user roles and deletion, remove downloads |

Jellyfin is the identity provider. A Jellyfin administrator is an admin in Pelicula; everyone else starts as a viewer. Admins invite people from Settings: create an invite, send the link (`/register?code=...`), and the recipient picks a username and password, which creates a Jellyfin account and signs them in. Invites are single-use and expire (default 72 hours).

## CLI

Run `./pelicula help` for the full list.

| Command | What it does |
|---|---|
| `pelicula up` | First run: terminal setup wizard. Then seed configs, start the stack, wait for VPN and health |
| `pelicula down` | Stop the stack |
| `pelicula status` | `docker compose ps` for the stack |
| `pelicula logs [svc]` | Follow logs, optionally for one service |
| `pelicula restart [svc]` | Restart one service or all of them |
| `pelicula update` | Pull images and rebuild, then recreate |
| `pelicula check-vpn` | Print the VPN public IP and forwarded port from gluetun |
| `pelicula reset-config [svc\|all]` | Delete seeded service configs (`all` = every service dir; `.env` is kept). Asks first; `--yes` skips |
| `pelicula doctor` | Container status, docker version, and recent logs of unhealthy containers, with secrets redacted |
| `pelicula version` | Print the CLI version |

Global flag: `--debug` enables verbose output.

## Folder layout

All three roots are set in `.env` and nothing is hardcoded.

```
CONFIG_DIR/                  service configs and runtime state
  sonarr/ radarr/ prowlarr/ qbittorrent/ jellyfin/ gluetun/
  pelicula/pelicula.db       the server's SQLite database
LIBRARY_DIR/                 your media (mounted at /media)
  movies/
  tv/
WORK_DIR/
  downloads/                 qBittorrent downloads (radarr/, tv-sonarr/)
```

The `pelicula` server mounts `LIBRARY_DIR` read-only. It never writes or deletes media; file removals go through the Sonarr and Radarr APIs.

## Testing

```bash
make test        # go test -race ./...
make vet         # go vet + gofmt check
make lint        # staticcheck
make e2e         # integration test: starts a real stack on port 7399 (needs Docker)
make playwright  # browser specs against PELICULA_URL (default http://localhost:7399)
```

`make e2e` runs `docker compose` with project `pelicula-test`, no VPN, and temporary folders, then tears everything down. It needs Docker and about 5 minutes for a cold build. The Playwright specs expect a running stack and the admin credentials in `PELICULA_ADMIN_USER` and `PELICULA_ADMIN_PASSWORD`; see [tests/playwright/README.md](tests/playwright/README.md).

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): processes, startup, data model, auth, pipeline, compose, nginx
- [docs/API.md](docs/API.md): HTTP routes, webhook contract, environment variables
- [docs/ROADMAP.md](docs/ROADMAP.md): what shipped and what was deferred
- [CHANGELOG.md](CHANGELOG.md)

## What this version deliberately leaves out

Each of these existed in the previous implementation or was planned. They were cut to keep one source of truth per fact and a codebase one person can read. [docs/ROADMAP.md](docs/ROADMAP.md) says why and what it would take to bring each back.

- Bazarr and subtitle acquisition, dual subtitles
- Transcoding and any other post-import action besides validation
- Backups and export/import of the watchlist
- Apprise notifications
- Hardware acceleration
- NFS-mounted library
- Local media import wizard
- Live updates over SSE (the dashboard polls)
- Open registration (invites only)
- Catalog browser, storage monitoring, notification bell
