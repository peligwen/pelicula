# Pelicula Roadmap

Pelicula's core phases (A–F) are shipped. This file tracks what's next, what's deferred, and summarises what landed.

---

## Active

### Bazarr — Subtitle Acquisition

Content arrives fully validated and transcoded, but subtitles are not yet automatic. Bazarr is the standard *arr-ecosystem solution and wires in cleanly alongside the existing auto-wire pattern.

- [ ] Add `bazarr` service to `docker-compose.yml` (Docker Compose profile, opt-in — same pattern as Jellyseerr and Apprise)
- [ ] Auto-wire in `middleware/autowire.go`: connect Bazarr to Sonarr and Radarr (mirror Prowlarr wiring), seed config with `UrlBase: /bazarr`
- [ ] Add nginx proxy at `/bazarr`
- [ ] Add Bazarr card to dashboard services grid
- [ ] Procula validation stage: after `catalog`, flag jobs missing subtitles for configured languages — Bazarr handles acquisition via its own Sonarr/Radarr polling; Procula does not talk to Bazarr directly
- [ ] `./pelicula configure` → Bazarr section: enable/disable

### Invite Flow (Phase D follow-up)

One-time invite links so admins can onboard users without creating their Jellyfin account manually first.

- [ ] `POST /api/pelicula/invites` — generate a signed, single-use token (HMAC, stored in `/config/pelicula/invites.json` with expiry + optional email label)
- [ ] `GET /api/pelicula/invites/accept?token=...` — validate token, create the Jellyfin account (name + password from the claimant), mark token used
- [ ] Dashboard: "Create invite link" button in the Users section → copies `http://host:7354/join?token=...` to clipboard (or shows it inline as with the existing share URL fallback)
- [ ] `/join` page (static HTML served by nginx) — a minimal form: choose username + password, submit to `/api/pelicula/invites/accept`
- [ ] Invite expiry: configurable TTL (default 7 days); expired/used tokens return a clear error page
- [ ] Notify admin (Apprise / internal feed) when an invite is claimed, so they know a new user has joined
- [ ] `./pelicula configure` → Users section: list active invites, revoke

**Notes:** Token must be unguessable (32-byte random, base64url-encoded). The `/join` page and `/api/pelicula/invites/accept` must be reachable without a Pelicula session (pre-auth). All other invite management endpoints are admin-guarded. No email sending from Pelicula itself — admin copies the link and sends it however they like.

### Pelicula for Windows

Replace the bash CLI (`./pelicula`) with a standalone Go binary (`pelicula` / `pelicula.exe`) for true cross-platform support including native Windows without WSL.

**Why:** The bash script is the only Windows-incompatible piece. All containers run fine on Docker Desktop for Windows. A Go CLI removes the bash + python3 dependencies entirely.

**Scope:**
- [ ] New `cmd/pelicula/` package — compiles to a single binary per platform
- [ ] All setup/configure prompts in Go (`bufio.Scanner`) — no bash required
- [ ] `.env` generation and config file writes in pure Go
- [ ] User management (`configure_users`) in Go — removes python3 dependency
- [ ] `docker compose` orchestration via `os/exec` (same as bash today)
- [ ] Platform detection in Go: Windows, macOS, Linux, Synology NAS
- [ ] TUN device handling on Linux/Synology; skip on macOS/Windows (Docker Desktop handles it)
- [ ] `docker-compose.override.yml` generation on non-macOS
- [ ] Distribute as a single binary — no shell, no interpreter, no dependencies

**What does NOT change:** middleware, procula, nginx, docker-compose.yml, all containers. The Go CLI is purely the operator tool that wraps `docker compose` and manages configuration.

**Note:** The current bash script remains authoritative until this is complete. Build after active phases above.

---

## Deferred

- **Jellyfin/Plex SSO**: layer on top of the Phase B user model. Delegates auth to Jellyfin or Plex; Pelicula user model is the standalone fallback.
- **Jellyfin as optional service**: acquisition-only mode for users who have their own media server (Plex, Emby, external Jellyfin). Jellyfin stays always-on until this is needed.
- **Retire/retention/storage pruning**: storage management and dedup reporting. Deferred, no timeline.
- **NFS-backed library (named volumes)**: host `movies/` and `tv/` on a NAS via NFS without a macOS Finder mount. Docker Desktop's Linux VM mounts the export directly through `local` volumes with `driver_opts: type=nfs`, so containers read/write it as normal named volumes — no `/Volumes`, no VirtioFS, no FUSE. Keep `WORK_DIR` (downloads + processing) local because NFS breaks hardlinks and is poorly suited to active torrent I/O; accept that Sonarr/Radarr will fall back to copy-on-import. Shape: new `docker-compose.nfs.yml` + `docker-compose.local-library.yml` override pair; `LIBRARY_NFS` / `NFS_HOST` / `NFS_EXPORT` / `NFS_OPTIONS` in `.env`; `./pelicula up` picks the right overlay. Full plan: `~/.claude/plans/shiny-floating-cosmos.md`.
- **Procula queue: JSON files vs SQLite** — Current implementation (`procula/queue.go`) uses one JSON file per job under `/config/procula/jobs/`. Pros: zero external dependencies, stdlib only, trivial to inspect, files are the unit of recovery. Cons: O(n) scans on load, no atomic multi-job operations. At current scale (single worker goroutine, hundreds of jobs/month) the cost is negligible. SQLite would win if we add cross-job analytics to the dashboard, a second worker, or job volume exceeds ~10k/month. Migration path is straightforward: JSON files are keyed by job ID, a one-shot importer can seed SQLite on first startup.

---

## Shipped

**Phase A — Onboarding:** Two-prompt setup (VPN key + country), `--advanced` walkthrough, `./pelicula configure` runtime menu, `set_env_var` helper, `$CONFIG_DIR/pelicula/` directory.

**Phase B — Auth & Roles:** `users.json` model with viewer / manager / admin roles, `Guard` / `GuardManager` / `GuardAdmin` middleware, dashboard login form, role-based UI hiding. Post-ship hardening: `IsOffMode()` guard on `handleUsers`, CSRF origin check, `MaxBytesReader`, username and UUID validation.

**Phase C — In-Dashboard Notifications:** Procula catalog stage writes to `/config/procula/notifications_feed.json` (ring buffer, 50 events), bell icon with unread badge, Processing section on dashboard with job cards and progress bars.

**Phase D — Jellyseerr:** Auto-wired to Jellyfin + Radarr + Sonarr on first boot, on by default, nginx proxy at `/jellyseerr`, dashboard Users section (list Jellyfin accounts, create accounts, share Jellyseerr URL).

**Phase E — Transcoding:** `procula/process.go` with FFmpeg progress tracking (parses `time=` from stderr), profile matching on codec or resolution, two default profiles shipped disabled (`compatibility-h264.json`, `mobile-1080p.json`).

**Phase F — External Notifications (Apprise):** Apprise container (opt-in Docker Compose profile), `direct` mode for single-webhook setups (ntfy, Gotify, any webhook URL), config at `/config/procula/notifications.json`. Discord is not a supported provider.
