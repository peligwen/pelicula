# Roadmap

## Principle

Every addition must be **a compose profile or a flag**, off by default, and must **not add a second source of truth**. If Sonarr, Radarr, qBittorrent, Jellyfin or `.env` already own a fact, the feature reads it from there instead of copying it into SQLite. A feature that needs its own copy of the catalog, of users, or of configuration is the wrong shape. Each round should also delete something.

## Shipped in v1

- One command to set up (`./pelicula up` runs a terminal wizard on first run) and one to run it.
- Eight containers (five without the VPN) behind one nginx port, 7354, with one compose file and a `vpn` profile.
- ProtonVPN/WireGuard through gluetun with qBittorrent and Prowlarr in its network namespace; forwarded port synced to qBittorrent automatically.
- Auto-wiring on startup: qBittorrent into Sonarr and Radarr, Sonarr and Radarr into Prowlarr, import webhooks, Jellyfin wizard, admin user and libraries. Idempotent.
- Seeded configs with external auth for the *arr apps, re-enforced on every `up`.
- Dashboard with Search, Requests, Jobs and Settings tabs; vanilla JS, no build step, light and dark.
- Unified search across Radarr and Sonarr; add directly (manager) or request (viewer); approve, decline or auto-approve.
- Jellyfin as the identity provider, a three-level role table, cookie sessions, single-use invites, CSRF origin check, nginx rate limits, `auth_request` gate for the *arr UIs.
- Import pipeline: webhook, job queue, ffprobe validation (integrity, sample, duration), request marked available, debounced Jellyfin refresh; on failure the release is blocklisted, only the matching bad file is deleted through the *arr API, and a new search starts.
- Server mounts the media library read-only; it never writes or deletes media.
- CLI: `up`, `down`, `status`, `logs`, `restart`, `update`, `check-vpn`, `reset-config`, `doctor`, `version`.
- Tests: Go unit tests, a Docker-based integration test, three Playwright specs; CI for all of them.

## Deliberately deferred

Each item below was cut from the rebuild on purpose. The line says why and what bringing it back would take, within the principle above.

- **Bazarr / subtitles.** Why: a seventh service with its own config, language profiles and wiring, and the old pipeline had several subtitle stages hanging off it. Would take: a `subtitles` compose profile adding Bazarr, an autowire step that connects it to Sonarr and Radarr, and nothing in SQLite; Bazarr already owns its languages.
- **Transcode action.** Why: it made the pipeline a media-processing system (progress, cancellation, disk accounting) rather than a validator. Would take: a flag that lets a failed or unsupported-codec job enqueue an ffmpeg run, with the original replaced only through the *arr API; the `jobs` table would gain a `kind`, not a second table.
- **Backups / export.** Why: it created a second serialization of the watchlist and settings that had to stay in sync with the schema. Would take: copy `CONFIG_DIR/pelicula/pelicula.db` (one file, SQLite `VACUUM INTO`) behind a `pelicula backup` command; the rest of the state lives in the *arr apps and `.env`, which already have their own backups.
- **Apprise notifications.** Why: another container and a settings surface for a feature the *arr apps already do natively. Would take: an `apprise` profile and one hook point (request available, job failed) in the server; or simply configure notifications inside Sonarr and Radarr.
- **Hardware acceleration.** Why: it is device- and host-specific (VAAPI, NVENC, VideoToolbox) and only matters for Jellyfin transcoding. Would take: a documented compose override or profile that passes `/dev/dri` or the NVIDIA runtime to Jellyfin; no server changes.
- **NFS-mounted library.** Why: it added a compose overlay pair, CLI validation and a volume-lifecycle special case. Would take: an optional generated override file, like the TUN one, that swaps the `/media` bind mount for an NFS volume; hardlinks across it still will not work, so `WORK_DIR` stays local.
- **Local import wizard.** Why: a large browser flow for a one-off task, and it duplicated what the *arr "Manual Import" screen does. Would take: nothing in the server; point Sonarr or Radarr's Manual Import at a folder under `/media`.
- **SSE live updates.** Why: a long-lived connection path through nginx, a fan-out hub in the server, and reconnection logic, for data that polling covers at 5 to 15 second intervals. Would take: one `/api/events` stream behind a flag; polling stays as the fallback.
- **Open registration.** Why: invites are the safer default on a LAN stack, and open sign-up needs abuse controls the server does not have. Would take: a `PELICULA_OPEN_REGISTRATION` flag in `.env` read by `POST /api/register`, plus a per-IP limit stricter than nginx's.
- **Dual subtitles.** Why: it depended on Bazarr and a stacked-ASS post-processing stage with a niche audience. Would take: Bazarr first, then a pipeline stage behind a flag that writes a sidecar next to the file.

Also left out, with no plan to restore: the catalog mirror and browser, libraries registry, storage monitoring, notification bell, missing-content watcher, docker-socket proxy and network drawer, loopback auto-admin session, and QR codes.
