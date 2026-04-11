# pelicula

One command to set up, one command to run. Search for movies and TV shows by name, stream with Jellyfin.

The rest is Pelicula.

## Statement from the Fleshie

Pelicula is the media stack I always wanted. Technology has advanced to the point that I can make software that I used to dream of.

There are many ambitious features that will take time to flesh out properly, but the core functionality of multi-user search->download->verify->catalog->watch is there. 

Use at your risk, and keep it LAN only - see the testing coverage table.

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

**Dual subtitles** — optional post-Bazarr pipeline stage that stacks two subtitle tracks into a single ASS sidecar file (e.g. `Movie.en-es.ass`) for language learners. Base language appears bottom-center; learning language appears top-center. Configure via `DUALSUB_ENABLED` / `DUALSUB_PAIRS` or the Procula settings UI. See [PROCULA.md](docs/PROCULA.md) for details.

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

## Feature Coverage

The table below lists every feature claimed in this README. **E2E** shows automated coverage from `tests/e2e.sh` and the Playwright specs in `tests/playwright/specs/`. **Manual** is a verification column for you to tick off yourself.

> **Note on CLI coverage:** `tests/e2e.sh` runs `docker compose` directly — it does not invoke the `pelicula` wrapper. CLI *effects* are often covered; the wrapper commands themselves are not.

| Feature | E2E | Manual |
|---------|-----|--------|
| **What Happens Automatically** | | |
| Seeds service configs (URL bases, auth bypass, download paths) | ✓ e2e.sh | ☐ |
| Starts 9 containers behind nginx reverse proxy | ~ partial (health check passes; container count not asserted) | ☐ |
| Waits for VPN connection and port forwarding | — | ☐ |
| Auto-wires qBittorrent as download client in Sonarr and Radarr | ✓ e2e.sh | ☐ |
| Connects Prowlarr indexers to Sonarr and Radarr | ✓ e2e.sh | ☐ |
| Validates downloads (FFprobe integrity, sample detection, blocklist + re-search) | ~ partial (pipeline via import webhook; blocklist/re-search path not exercised) | ☐ |
| Missing content watcher (2-min interval auto-search) | — | ☐ |
| Enforces auth bypass on every start | — | ☐ |
| **Dashboard** | | |
| Unified search (Sonarr + Radarr in parallel, interleaved, type filter tabs) | — | ☐ |
| One-click add (search starts immediately) | — | ☐ |
| Watch button links directly to Jellyfin | ~ partial (Jellyfin library populated + searchable; button click not asserted) | ☐ |
| Download management (pause / resume / cancel / blocklist) | — | ☐ |
| Processing pipeline live status with progress bars | ✓ playwright (import-play: pipeline lane card → completed) | ☐ |
| Notifications bell icon with unread count | — | ☐ |
| Storage monitoring (per-volume usage bars, growth rate, time-to-full) | — | ☐ |
| Service awareness (search disables with red/yellow when *arr down) | — | ☐ |
| VPN telemetry (IP, country, forwarded port, transfer speeds) | — | ☐ |
| Service status indicators (red until up, green when healthy) | — | ☐ |
| Collapse search results to top result / "Show N more" expand | — | ☐ |
| **Services** | | |
| `/` — Dashboard | ✓ e2e.sh (public route + Cache-Control: no-store) | ☐ |
| `/setup` — Browser setup wizard | — | ☐ |
| `/settings` — Runtime configuration | ✓ e2e.sh (protected route redirect + cookie access) | ☐ |
| `/import` — Local media import wizard | ✓ playwright (import-play exercises full wizard) | ☐ |
| `/api/pelicula/` — Go middleware | ✓ e2e.sh (health, status, auth, hooks/import) | ☐ |
| `/api/procula/` — Procula pipeline | ✓ e2e.sh (settings + jobs polling) | ☐ |
| `/api/vpn/` — Gluetun VPN telemetry | — | ☐ |
| `/sonarr/` | ~ partial (auto-wire confirms reachable; UI not exercised) | ☐ |
| `/radarr/` | ~ partial (same) | ☐ |
| `/prowlarr/` | ~ partial (protected route redirect only) | ☐ |
| `/qbt/` | ~ partial (protected route redirect only) | ☐ |
| `/jellyfin/` | ✓ e2e.sh + ✓ playwright (library search + catalog sync) | ☐ |
| **Download Management** | | |
| Pause torrent | — | ☐ |
| Resume paused torrent | — | ☐ |
| Cancel (removes torrent + files, unmonitors in *arr) | — | ☐ |
| Blocklist release with reason selection | — | ☐ |
| Progress bar colours (green active / amber paused / blue seeding) | — | ☐ |
| **Content Requests** | | |
| Viewer submits request from search results | — | ☐ |
| Admin approves / denies with reason | — | ☐ |
| Approval auto-adds to Radarr or Sonarr with configured quality profile | — | ☐ |
| Request flips to "available" on import + Apprise notifies requester | — | ☐ |
| Admin generates shareable invite link | — | ☐ |
| Invite redeem (username + password → Jellyfin viewer account) | — | ☐ |
| **Optional Services** | | |
| Apprise push notifications (phone, email, Telegram, ntfy, Gotify, 85+ services) | — | ☐ |
| Bazarr auto subtitle acquisition (wired to Sonarr + Radarr on startup) | ✓ playwright (subtitle-acquisition spec: await_subs stage → completed → jellyfin_synced) | ☐ |
| Dual subtitles (stacked ASS sidecar, base + learning language) | ~ partial (pipeline stage runs; output file not asserted) | ☐ |
| **Auth** | | |
| `PELICULA_AUTH=off` (no login required) | ~ partial (e2e toggles; only jellyfin-mode behaviour asserted end to end) | ☐ |
| `PELICULA_AUTH=jellyfin` (credentials verified against Jellyfin) | ✓ e2e.sh (login 401/200, session cookie, protected routes, logout) | ☐ |
| Jellyfin admins automatically get admin role in Pelicula | — | ☐ |
| Role capabilities: viewer / manager / admin | — | ☐ |
| **CLI** | | |
| `pelicula up` | — (effects tested; wrapper not invoked by e2e) | ☐ |
| `pelicula down` | — | ☐ |
| `pelicula status` | — | ☐ |
| `pelicula logs [svc]` | — | ☐ |
| `pelicula check-vpn` | — | ☐ |
| `pelicula update` | — | ☐ |
| `pelicula restart [svc]` / `restart-acquire` | — | ☐ |
| `pelicula rebuild` | — | ☐ |
| `pelicula reset-config [svc]` | — | ☐ |
| `pelicula import` | — | ☐ |
| `pelicula export` / `import-backup` | — | ☐ |
| `pelicula test` | ✓ e2e.sh (this command runs the suite) | ☐ |
| **Security** | | |
| Peligrosa remote access (Jellyfin-only, TLS) | — | ☐ |

> **Claimed in other docs — candidates to promote or drop from README:**
> - Open registration toggle (`PELICULA_OPEN_REGISTRATION`, LAN-only) — [PELIGROSA.md](docs/PELIGROSA.md)
> - Peligrosa cert modes: Let's Encrypt / BYO cert / self-signed — [PELIGROSA.md](docs/PELIGROSA.md)
> - Remote role capping (admin forced to viewer on remote vhost) — [PELIGROSA.md](docs/PELIGROSA.md)
> - Backup export/import v1→v2 auto-migration — [API.md](docs/API.md)
> - Now-playing Jellyfin sessions card — [API.md](docs/API.md)
> - Notifications feed merges Procula + *arr history — [API.md](docs/API.md)
> - Server-side folder browser + library scan/apply — [API.md](docs/API.md) / [ARCHITECTURE.md](docs/ARCHITECTURE.md)
> - Storage warning/critical thresholds — [PROCULA.md](docs/PROCULA.md)
> - Jellyfin alternate-version sidecars from transcoding profiles — [PROCULA.md](docs/PROCULA.md)

## Development

```
make install-hooks   # one-time: sets up pre-commit, pre-push, and pre-merge-commit hooks
make test            # unit tests (all 3 modules)
make verify          # unit tests + e2e integration suite (~10 min)
```

## License

AGPL-3.0 — see [LICENSE](LICENSE). If you run a modified version of Pelicula
as a network service, you must make your source available to its users.

## Post-Setup: Add Indexers

1. Open Prowlarr at `http://localhost:7354/prowlarr/`
2. Go to **Indexers** > **Add Indexer**
3. Add your preferred torrent indexers
4. Indexers automatically sync to Sonarr and Radarr

Once indexers are configured, search and download from the dashboard.
