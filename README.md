# pelicula

Automated media stack — search for shows and movies by name, download via torrent through a VPN, stream with Jellyfin. Runs on macOS or Synology NAS.

## Quick Start

```bash
git clone https://github.com/your-user/pelicula.git
cd pelicula
./pelicula setup    # answer a few prompts (~30 seconds)
./pelicula up       # pulls images, builds middleware, starts everything
```

Open `http://localhost:7354` — that's it. Services are auto-wired on first boot.

## Prerequisites

- **Docker** (Docker Desktop on Mac, Container Manager on Synology)
- **ProtonVPN** paid plan (Plus or higher) — free tier doesn't support P2P or port forwarding
- A **Wireguard private key** from ProtonVPN (the setup wizard walks you through this)

## What You Get

Everything runs behind an nginx reverse proxy on port **7354** (PELI on a phone keypad):

| Service | URL | Purpose |
|---------|-----|---------|
| Dashboard | `http://localhost:7354/` | Unified search, downloads, VPN telemetry, service status |
| Sonarr | `http://localhost:7354/sonarr/` | TV show search & automation |
| Radarr | `http://localhost:7354/radarr/` | Movie search & automation |
| Prowlarr | `http://localhost:7354/prowlarr/` | Torrent indexer manager |
| qBittorrent | `http://localhost:7354/qbt/` | Torrent client (VPN-only traffic) |
| Jellyfin | `http://localhost:7354/jellyfin/` | Media server & streaming |

Port is configurable via `PELICULA_PORT` in `.env`.

All torrent traffic is routed through ProtonVPN Wireguard with automatic port forwarding. If the VPN drops, qBittorrent loses internet (kill-switch).

## Auto-Wiring

On first `./pelicula up`, the Go middleware automatically:
- Adds qBittorrent as the download client in Sonarr and Radarr
- Sets root folders (`/tv` for Sonarr, `/movies` for Radarr)
- Connects Prowlarr to Sonarr and Radarr
- Configures URL bases and auth bypass for all services

The only manual step is **adding indexers in Prowlarr** — the dashboard shows a warning if none are configured.

## Dashboard

The dashboard at `http://localhost:7354/` includes:
- **VPN telemetry** — IP, country, forwarded port, download/upload speeds
- **Unified search** — searches both Sonarr (TV) and Radarr (movies) simultaneously, with type filter tabs
- **One-click add** — add movies or shows to your library directly from search results
- **Download progress** — active torrents with progress bars, speeds, ETA
- **Service status** — green/red indicators for all services
- **Indexer warning** — yellow toast if no indexers are configured

## CLI Reference

```
./pelicula setup       # Interactive first-time setup
./pelicula up          # Start all services
./pelicula down        # Stop all services
./pelicula status      # Show container status
./pelicula logs [svc]  # Tail logs (optionally for one service)
./pelicula check-vpn   # Verify VPN tunnel, port forwarding, service health
./pelicula update      # Pull latest images and recreate containers
```

## Architecture

```
  http://localhost:7354
        │
  nginx reverse proxy
        │
        ├── /                → Dashboard (search, downloads, status)
        ├── /api/pelicula/   → Go middleware (auto-wiring, search API, downloads API)
        ├── /api/vpn/        → Gluetun control API (VPN telemetry)
        ├── /sonarr/         → Sonarr ── TV show automation ──┐
        ├── /radarr/         → Radarr ── movie automation ────┤
        ├── /prowlarr/       → Prowlarr ◄── indexer search ◄──┘
        ├── /qbt/            → qBittorrent
        │                          │
        │                          ▼ all traffic through VPN
        │                    Gluetun (ProtonVPN Wireguard)
        │                          │
        │                          ▼ downloads to
        │                    /downloads → moved to /movies or /tv
        │                          │
        └── /jellyfin/       → Jellyfin ◄── streams your library
```

## Platform Notes

| | macOS | Synology NAS |
|---|---|---|
| Config location | `./config` | `/volume1/docker/media-stack/config` |
| Media location | `~/media` | `/volume1/media` |
| Docker sudo | no | yes (handled automatically) |
| TUN device | not needed | created during setup |

All paths are configurable during `./pelicula setup`.

## Optional Auth

Set `PELICULA_AUTH=true` and `PELICULA_PASSWORD=yourpassword` in `.env` to require a password for the dashboard and API. Services are still accessible directly via their paths.

## Post-Setup: Add Indexers

The only manual step after `./pelicula up` is adding torrent indexers:

1. Open Prowlarr at `http://localhost:7354/prowlarr/`
2. Go to **Indexers** → **Add Indexer**
3. Add your preferred torrent indexers
4. Indexers automatically sync to Sonarr and Radarr

Once indexers are configured, you can search and download directly from the dashboard.

See [synology-media-stack.md](synology-media-stack.md) for detailed manual setup reference.
