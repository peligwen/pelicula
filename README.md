# pelicula

Search for movies and TV shows by name, download via torrent through a VPN, stream with Jellyfin. One command to set up, one command to run.

## Quick Start

```bash
git clone https://github.com/peligwen/pelicula.git
cd pelicula
./pelicula setup    # answer a few prompts
./pelicula up       # pulls images, builds middleware, starts everything
```

Open `http://localhost:7354` — that's it.

Prefer a browser? Run `./pelicula up` and open `http://localhost:7354/setup` — a setup wizard walks you through configuration without touching the terminal.

## Prerequisites

- **Docker** with Compose v2 (Docker Desktop on macOS/Windows, or Docker Engine on Linux)
- **ProtonVPN** paid plan (Plus or higher) with a Wireguard private key
- **bash** (macOS, Linux, WSL — the CLI auto-detects your platform)

## What Happens Automatically

On `./pelicula up`, the stack:

1. Seeds service configs (URL bases, auth bypass, download paths)
2. Starts 9 containers behind an nginx reverse proxy on port 7354
3. Waits for VPN connection and port forwarding
4. Auto-wires qBittorrent as the download client in Sonarr and Radarr
5. Connects Prowlarr indexers to both Sonarr and Radarr
6. Validates completed downloads automatically (FFprobe integrity, sample detection) — bad files are blocklisted and re-searched
7. Watches for newly added content and triggers searches automatically
8. Enforces auth bypass on every start (services can't lock you out)

The only manual step is **adding indexers in Prowlarr** — the dashboard warns if none are configured.

## Dashboard

The dashboard at `http://localhost:7354/` is the single interface for the whole stack:

- **Unified search** — searches Sonarr and Radarr in parallel, interleaved results, type filter tabs
- **One-click add** — add movies or shows and search starts immediately
- **Watch button** — links directly to Jellyfin when content is ready to stream
- **Download management** — pause, resume, cancel, or blocklist with reason selection
- **Processing pipeline** — live validation and transcoding status with progress bars
- **Notifications** — bell icon with unread count for "content ready" and validation events
- **Storage monitoring** — per-volume usage bars with growth rate and time-to-full estimates
- **Service awareness** — search disables with a red/yellow warning when Radarr or Sonarr are down
- **VPN telemetry** — IP, country, forwarded port, transfer speeds
- **Service status** — red until confirmed up, green when healthy

When you click away from search results, they collapse to the top result with a "Show N more" bar. Click back to expand.

## Services

Everything runs behind nginx on one port:

| Path | Service | Purpose |
|------|---------|---------|
| `/` | Dashboard | Search, downloads, processing, status |
| `/setup` | Setup wizard | Browser-based first-time configuration |
| `/settings` | Settings | Runtime configuration |
| `/import` | Import wizard | Browse and import local media files |
| `/api/pelicula/` | Go middleware | Auto-wiring, search API, download actions |
| `/api/procula/` | Procula | Media processing pipeline |
| `/api/vpn/` | Gluetun | VPN telemetry |
| `/sonarr/` | Sonarr | TV show automation |
| `/radarr/` | Radarr | Movie automation |
| `/prowlarr/` | Prowlarr | Indexer management |
| `/qbt/` | qBittorrent | Torrent client (VPN-only traffic) |
| `/jellyfin/` | Jellyfin | Media server and streaming |
| `/jellyseerr/` | Jellyseerr | Multi-user request management (opt-in) |

All torrent traffic goes through Gluetun's Wireguard tunnel. If the VPN drops, qBittorrent loses internet (kill-switch).

## CLI

```
./pelicula setup       # Interactive first-time configuration
./pelicula up          # Start all services
./pelicula down        # Stop all services
./pelicula status      # Show container status
./pelicula logs [svc]  # Tail logs (optionally for one service)
./pelicula check-vpn   # Verify VPN tunnel and service health
./pelicula update      # Pull latest images and recreate
./pelicula test        # End-to-end integration test (isolated stack, no VPN needed)
```

## Architecture

```
  http://localhost:7354
        |
  nginx reverse proxy
        |
        +-- /                -> Dashboard (static HTML)
        +-- /setup           -> Browser setup wizard
        +-- /settings        -> Runtime configuration
        +-- /import          -> Local media import wizard
        +-- /api/pelicula/   -> Go middleware (search, downloads, auto-wire)
        +-- /api/procula/    -> Procula (validation, transcoding, storage)
        +-- /api/vpn/        -> Gluetun control API
        +-- /sonarr/         -> Sonarr --+
        +-- /radarr/         -> Radarr --+--> Prowlarr (indexers)
        +-- /qbt/            -> qBittorrent
        |                         |
        |                    Gluetun (VPN tunnel)
        |                         |
        |                    /downloads -> /movies, /tv
        |
        +-- /jellyfin/       -> Jellyfin (streams your library)
        +-- /jellyseerr/     -> Jellyseerr (request management, opt-in)
```

## Download Management

The dashboard download panel supports:

| Action | What it does |
|--------|-------------|
| **Pause** | Stops the torrent in qBittorrent. Reversible. |
| **Resume** | Resumes a paused torrent. |
| **Cancel** | Removes torrent + files, unmonitors in Radarr/Sonarr so the watcher won't re-grab it. |
| **Blocklist** | Removes and blocklists the release with a reason (wrong quality, wrong language, corrupt, slow, wrong content, other). |

Progress bars are green (active), amber (paused), or blue (seeding).

## Missing Content Watcher

A background process checks every 2 minutes for monitored movies/episodes that have no files and aren't already downloading. If found, it triggers a search automatically. This means content added through any path (dashboard, Radarr UI, Sonarr UI, API) gets searched without manual intervention.

## Sharing with Others (Jellyseerr)

Jellyseerr is included and on by default. It gives friends and family a clean interface to request movies and TV shows without accessing the admin tools directly.

**How it works:**
1. Pelicula auto-wires Jellyseerr to Jellyfin, Radarr, and Sonarr on first boot.
2. Create Jellyfin accounts for your users from the **Users** section of the Pelicula dashboard (or in the Jellyfin admin UI at `/jellyfin`).
3. Share your Jellyseerr URL (`http://your-host:5055/`) — users log in with their Jellyfin credentials and can start requesting content immediately.

Requests route through Jellyseerr's approval workflow before hitting Sonarr/Radarr. Admin searches from the Pelicula dashboard bypass Jellyseerr and go directly to the *arr apps.

To disable Jellyseerr: run `./pelicula configure`, choose **Jellyseerr**, and select disable. Searches will fall back to direct *arr calls.

## Optional Services

**Apprise** — push notifications to phone, email, Telegram, ntfy, Gotify, and 85+ other services. Configure notification URLs via `./pelicula configure`.

## Optional Auth

Set `PELICULA_AUTH=true` and `PELICULA_PASSWORD=yourpassword` in `.env` to require a password for the dashboard and API.

## Post-Setup: Add Indexers

1. Open Prowlarr at `http://localhost:7354/prowlarr/`
2. Go to **Indexers** > **Add Indexer**
3. Add your preferred torrent indexers
4. Indexers automatically sync to Sonarr and Radarr

Once indexers are configured, search and download from the dashboard.
