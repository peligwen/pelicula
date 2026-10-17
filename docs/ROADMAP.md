# Roadmap

## Principle

Every addition must be **a compose profile or a flag**, off by default, and must **not add a second source of truth**. If Sonarr, Radarr, qBittorrent, Jellyfin or `.env` already own a fact, the feature reads it from there instead of copying it into SQLite. A feature that needs its own copy of the catalog, of users, or of configuration is the wrong shape. Each round should also delete something.

Pelicula serves one person: the administrator acquires media from the dashboard on the LAN, and everyone else watches through Jellyfin. Anything that needs a second kind of user, or a second surface reachable from the internet, is out of scope.

## Shipped in v1

- One command to set up (`./pelicula up` runs a terminal wizard on first run) and one to run it.
- Eight containers (five without the VPN) behind one nginx port, 7354, with one compose file and a `vpn` profile.
- ProtonVPN/WireGuard through gluetun with qBittorrent and Prowlarr in its network namespace; forwarded port synced to qBittorrent automatically.
- Auto-wiring on startup: qBittorrent into Sonarr and Radarr, Sonarr and Radarr into Prowlarr, import webhooks, Jellyfin wizard, admin user and libraries. Idempotent.
- Seeded configs with external auth for the *arr apps, re-enforced on every `up`.
- Dashboard with Search, Jobs and Settings tabs; vanilla JS, no build step, light and dark.
- Unified search across Radarr and Sonarr; add a title with one click.
- Jellyfin as the identity provider for the one administrator, cookie sessions, CSRF origin check, nginx rate limit on login, `auth_request` gate for the *arr UIs.
- Import pipeline: webhook, job queue, ffprobe validation (integrity, sample, duration), debounced Jellyfin refresh; on failure the release is blocklisted, only the matching bad file is deleted through the *arr API, and a new search starts. Failed jobs can be retried.
- Server mounts the media library read-only; it never writes or deletes media.
- CLI: `up`, `down`, `status`, `logs`, `restart`, `update`, `check-vpn`, `reset-config`, `doctor`, `seed`, `version`.
- Tests: Go unit tests, a Docker-based integration test, two Playwright specs; CI for all of them.

## Next

The two additions the single-admin scope still wants. Neither is in the tree yet; both must arrive in the shape the principle above describes.

- **Subtitles.** Would take: a `subtitles` compose profile adding Bazarr, an autowire step that connects it to Sonarr and Radarr, and nothing in SQLite; Bazarr already owns its languages. The dashboard would at most link to it the way it links to the *arr UIs. A pipeline stage that extracts or converts embedded subtitles with ffmpeg could follow behind a flag, writing sidecars only through a path the *arr apps already own.
- **Library scan and reprocess.** Today validation runs only on import. Would take: a `POST /api/jobs/scan` route (behind a flag) that walks Sonarr's and Radarr's file lists through their APIs, enqueues a job per file not yet validated, and reuses the existing worker, retry and failure handling. No second catalog: the file list comes from the *arr apps on every scan.

## Deliberately deferred

Each item below was cut on purpose. The line says why and what bringing it back would take, within the principle above.

- **Requests, roles and invites.** Why: they existed for viewers who ask the administrator for titles, and the dashboard is now for the administrator alone; Jellyfin already creates and removes viewer accounts. Would take: a second user kind in the auth layer, a requests table, approval routes and a tab. Bring it back only if a second person actually needs to acquire media.
- **Remote registration.** Why: port 7354 is LAN-only HTTP, so a register page there helps nobody outside; exposing it means a second internet-facing surface with its own TLS, rate limits and abuse controls. Would take: a separate listener behind the operator's own TLS terminator, invite codes stored outside the dashboard's trust boundary, and a decision to widen the threat model. Until then, give people Jellyfin accounts from Jellyfin.
- **Transcode action.** Why: it made the pipeline a media-processing system (progress, cancellation, disk accounting) rather than a validator. Would take: a flag that lets a failed or unsupported-codec job enqueue an ffmpeg run, with the original replaced only through the *arr API; the `jobs` table would gain a `kind`, not a second table.
- **Backups / export.** Why: it created a second serialization of settings that had to stay in sync with the schema. Would take: copy `CONFIG_DIR/pelicula/pelicula.db` (one file, SQLite `VACUUM INTO`) behind a `pelicula backup` command; the rest of the state lives in the *arr apps and `.env`, which already have their own backups.
- **Apprise notifications.** Why: another container and a settings surface for a feature the *arr apps already do natively. Would take: an `apprise` profile and one hook point (job failed) in the server; or simply configure notifications inside Sonarr and Radarr.
- **Hardware acceleration.** Why: it is device- and host-specific (VAAPI, NVENC, VideoToolbox) and only matters for Jellyfin transcoding. Would take: a documented compose override or profile that passes `/dev/dri` or the NVIDIA runtime to Jellyfin; no server changes.
- **NFS-mounted library.** Why: it added a compose overlay pair, CLI validation and a volume-lifecycle special case. Would take: an optional generated override file, like the TUN one, that swaps the `/media` bind mount for an NFS volume; hardlinks across it still will not work, so `WORK_DIR` stays local.
- **Local import wizard.** Why: a large browser flow for a one-off task, and it duplicated what the *arr "Manual Import" screen does. Would take: nothing in the server; point Sonarr or Radarr's Manual Import at a folder under `/media`.
- **SSE live updates.** Why: a long-lived connection path through nginx, a fan-out hub in the server, and reconnection logic, for data that polling covers at 5 to 15 second intervals. Would take: one `/api/events` stream behind a flag; polling stays as the fallback.
- **Dual subtitles.** Why: it depended on Bazarr and a stacked-ASS post-processing stage with a niche audience. Would take: Bazarr first, then a pipeline stage behind a flag that writes a sidecar next to the file.

Also left out, with no plan to restore: the catalog mirror and browser, libraries registry, storage monitoring, notification bell, missing-content watcher, docker-socket proxy and network drawer, loopback auto-admin session, QR codes, open registration, and the dashboard's own user management.
