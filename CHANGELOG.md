# Changelog

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## v1 rescope (unreleased)

A from-scratch rebuild whose goal is to delete complexity while keeping the core loop: search, request, download, verify, watch. It replaces the previous implementation, which remains in git history under the `pre-rescope` tag.

### Changed

- **One Go module, two binaries.** `cmd/pelicula` (stdlib-only CLI) and `cmd/pelicula-server` (API, auth, auto-wiring, import pipeline, port sync). The separate `middleware` and `procula` services and their modules are replaced by the single `pelicula` server container.
- **One SQLite database with six tables** (`roles`, `sessions`, `invites`, `requests`, `jobs`, `settings`). Sonarr, Radarr, qBittorrent and Jellyfin remain the source of truth for the catalog, downloads and users; the server reads through to them instead of mirroring.
- **Eight containers, five without the VPN** (was eleven). One compose file with a `vpn` profile for gluetun, qBittorrent and Prowlarr. The only generated overlay is the TUN device mapping on Linux.
- **Terminal setup wizard** replaces the browser setup wizard. `.env` is written only by the CLI.
- **Auth** is Jellyfin login plus a `roles` table and cookie sessions. Route guards by role (viewer, manager, admin); `auth_request` gate for the *arr UIs; nginx rate limits login and register.
- **Dashboard** is four tabs (Search, Requests, Jobs, Settings), vanilla JS with no build step and no inline scripts.
- **Validation** reduced to one ffprobe pass (integrity, sample, duration). A failure blocklists the release, deletes only the matching file through the *arr API, and searches again. The server mounts `/media` read-only.
- **Tests**: Go unit tests per package, a Docker-based integration test (`make e2e`), and three Playwright specs. The bash verify suites and `tests/e2e.sh` are gone. CI runs gofmt, vet, staticcheck, `go test -race`, shellcheck and the integration test.
- Go 1.25.

### Removed

- Procula and its transcoding, storage monitoring, catalog and action bus.
- Bazarr, subtitle acquisition and dual subtitles.
- Apprise notifications and the notification bell.
- docker-socket-proxy (the server no longer talks to Docker) and the network dashboard drawer.
- NFS library overlays, `libraries.json` and the libraries registry.
- Catalog mirror and catalog browser, journey timeline, missing-content watcher.
- Backups: `pelicula export` and `pelicula import-backup`.
- Local import wizard: `pelicula import`.
- SSE live updates (the dashboard polls).
- Open registration, QR codes, and the loopback auto-admin session.
- CLI commands `rebuild`, `redeploy`, `restart-acquire`, `test`, `verify`, image-skew detection and the browser setup wizard.
- Compose overlays other than the generated TUN override.
