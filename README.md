# pelicula

Search for movies and TV shows by name, download via torrent through a VPN, stream with Jellyfin. One command to set up, one command to run.

## Quick Start

```bash
git clone https://github.com/peligwen/pelicula.git
cd pelicula
./pelicula up       # builds CLI, runs setup wizard on first run, starts everything
```

Open `http://localhost:7354` — that's it. On first run, a browser-based setup wizard walks you through configuration.

## Prerequisites

- **Docker** with Compose v2 (Docker Desktop on macOS/Windows, or Docker Engine on Linux)
- **Go 1.23+** (the wrapper script auto-builds the CLI on first run)
- **ProtonVPN** paid plan (Plus or higher) with a Wireguard private key
- **bash** (macOS, Linux, WSL, Synology NAS — the CLI auto-detects your platform and uses the right default paths; no manual folder creation needed on Synology)

## What Happens Automatically

On `pelicula up`, the stack:

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

All torrent traffic goes through Gluetun's Wireguard tunnel. If the VPN drops, qBittorrent loses internet (kill-switch).

## CLI

```
pelicula up                  # Start all services (runs setup wizard on first run)
pelicula down                # Stop all services
pelicula status              # Show container status
pelicula logs [svc]          # Tail logs (optionally for one service)
pelicula check-vpn           # Verify VPN tunnel and service health
pelicula update              # Pull latest images and recreate
pelicula restart [svc]       # Restart service(s) without stopping the whole stack
pelicula restart-acquire     # Restart and re-run VPN port-forward acquisition
pelicula rebuild             # Rebuild and restart middleware/procula containers
pelicula reset-config [svc]  # Delete seeded configs so they regenerate on next up
pelicula import              # Open the browser-based local media import wizard
pelicula export              # Export watchlist/library backup
pelicula import-backup       # Restore from a backup exported by pelicula export
pelicula test                # End-to-end integration test (isolated stack, no VPN needed)
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

## Content Requests

Viewers can request movies and TV shows directly from the dashboard search results. Admins approve or deny requests from the Requests section; approval automatically adds the item to Radarr or Sonarr using the quality profile and root folder configured in the Requests settings panel. When the download completes and is imported, the request flips to "available" and Apprise notifies the requester.

**How it works:**
1. Generate an invite link from the **Users** section and share it with your viewers.
2. Recipients redeem the link, set a username and password, and log in.
3. Viewers search for a title and click **Request** — the request appears in the admin's Requests section.
4. Admin approves (or denies with a reason) from the dashboard. No external tools needed.

## Optional Services

**Apprise** — push notifications to phone, email, Telegram, ntfy, Gotify, and 85+ other services. Configure notification URLs in the Settings page.

**Bazarr** — automatic subtitle acquisition from OpenSubtitles, Addic7ed, Podnapisi, and others. Wired to Sonarr and Radarr automatically on startup. Set which languages to acquire in the Settings page (`PELICULA_SUB_LANGS`).

**Dual subtitles** — optional post-Bazarr pipeline stage that stacks two subtitle tracks into a single ASS sidecar file (e.g. `Movie.en-es.ass`) for language learners. Base language appears bottom-center; learning language appears top-center. Configure via `DUALSUB_ENABLED` / `DUALSUB_PAIRS` or the Procula settings UI. See [PROCULA.md](PROCULA.md) for details.

## Auth

Pelicula supports two modes via `PELICULA_AUTH` in `.env`:

- `off` — no login required. Fine on a trusted LAN.
- `jellyfin` (default) — credentials verified against Jellyfin. Roles stored locally in `roles.json`; Jellyfin admins automatically get admin in Pelicula.

Role capabilities: **viewer** sees the dashboard and can submit content requests; **manager** can search, add content, and pause/resume downloads; **admin** has full access including settings, *arr UIs, and destructive actions (cancel, blocklist, user management).

**Invites:** Admins can generate shareable invite links from the Users section of the dashboard. Recipients open the link, choose a username and password, and get a Jellyfin viewer account automatically. No admin involvement after the link is shared.

## Security

Pelicula is designed for a trusted LAN. See [SECURITY.md](SECURITY.md) for
the threat model, known limitations, and how to report a vulnerability
privately. The opt-in Peligrosa remote access feature exposes **only
Jellyfin** over TLS — never the admin stack.

## License

AGPL-3.0 — see [LICENSE](LICENSE). If you run a modified version of Pelicula
as a network service, you must make your source available to its users.

## Post-Setup: Add Indexers

1. Open Prowlarr at `http://localhost:7354/prowlarr/`
2. Go to **Indexers** > **Add Indexer**
3. Add your preferred torrent indexers
4. Indexers automatically sync to Sonarr and Radarr

Once indexers are configured, search and download from the dashboard.
