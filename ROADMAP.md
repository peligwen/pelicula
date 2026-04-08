# Pelicula Roadmap

Pelicula's core phases (A–F) are shipped. This file tracks what's next, what's deferred, and summarises what landed.

---

## Active

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

## Peligrosa Initiative

Security and user-interaction safety hardening. See [PELIGROSA.md](PELIGROSA.md) for the full threat model and current surface.

- [ ] **[Peligrosa] bcrypt/argon2id** — replace salted SHA-256 KDF with a proper slow hash for user passwords (`users` mode only). SHA-256 is fast on GPUs; argon2id is the preferred migration target. Not applicable to `jellyfin` mode (Jellyfin owns hashing).
- [ ] **[Peligrosa] HMAC invite tokens** — sign tokens with a server secret so validity is verifiable without a DB lookup. Prevents brute-force token enumeration.
- [x] **[Peligrosa] Central CSRF middleware** — `requireLocalOriginStrict` / `requireLocalOriginSoft` wired per-route in `main.go`, replacing 8 inline checks across 5 files.
- [ ] **[Peligrosa] `middleware/peligrosa/` subpackage** — extract auth, invites, requests, user CRUD, and webhook validation into a Go subpackage with an explicit API surface. Blocked on `JellyfinClient` interface, `Fulfiller` injection, and shared HTTP helpers extraction. See plan file for incremental path.
- [x] **[Peligrosa] Jellyfin SSO** — `PELICULA_AUTH=jellyfin`: credentials verified against Jellyfin's `/Users/AuthenticateByName`; roles (no passwords) stored in `roles.json`; Jellyfin admins auto-promoted. Plex SSO deferred.
- [ ] **[Peligrosa] Plex SSO** — deferred; different API shape (plex.tv OAuth dance).

---

## Deferred

- **Invite Apprise notification**: notify admin via Apprise/internal feed when an invite is claimed. Deferred; low priority since the dashboard shows active invites and redemption history.
- **`./pelicula configure` invite management**: list active invites and revoke them from the CLI menu. Deferred; the dashboard Users section covers this via the UI.

- **Plex SSO**: moved to Peligrosa initiative above (Jellyfin SSO shipped).
- **Jellyfin as optional service**: acquisition-only mode for users who have their own media server (Plex, Emby, external Jellyfin). Jellyfin stays always-on until this is needed.
- **Retire/retention/storage pruning**: storage management and dedup reporting. Deferred, no timeline.
- **NFS-backed library (named volumes)**: host `movies/` and `tv/` on a NAS via NFS without a macOS Finder mount. Docker Desktop's Linux VM mounts the export directly through `local` volumes with `driver_opts: type=nfs`, so containers read/write it as normal named volumes — no `/Volumes`, no VirtioFS, no FUSE. Keep `WORK_DIR` (downloads + processing) local because NFS breaks hardlinks and is poorly suited to active torrent I/O; accept that Sonarr/Radarr will fall back to copy-on-import. Shape: new `docker-compose.nfs.yml` + `docker-compose.local-library.yml` override pair; `LIBRARY_NFS` / `NFS_HOST` / `NFS_EXPORT` / `NFS_OPTIONS` in `.env`; `./pelicula up` picks the right overlay. Full plan: `~/.claude/plans/shiny-floating-cosmos.md`.
- **Procula queue: JSON files vs SQLite** — Current implementation (`procula/queue.go`) uses one JSON file per job under `/config/procula/jobs/`. Pros: zero external dependencies, stdlib only, trivial to inspect, files are the unit of recovery. Cons: O(n) scans on load, no atomic multi-job operations. At current scale (single worker goroutine, hundreds of jobs/month) the cost is negligible. SQLite would win if we add cross-job analytics to the dashboard, a second worker, or job volume exceeds ~10k/month. Migration path is straightforward: JSON files are keyed by job ID, a one-shot importer can seed SQLite on first startup.

---

## Shipped

**Phase A — Onboarding:** Two-prompt setup (VPN key + country), `--advanced` walkthrough, `./pelicula configure` runtime menu, `set_env_var` helper, `$CONFIG_DIR/pelicula/` directory.

**Phase B — Auth & Roles:** `users.json` model with viewer / manager / admin roles, `Guard` / `GuardManager` / `GuardAdmin` middleware, dashboard login form, role-based UI hiding. Post-ship hardening: `IsOffMode()` guard on `handleUsers`, CSRF origin check, `MaxBytesReader`, username and UUID validation.

**Phase C — In-Dashboard Notifications:** Procula catalog stage writes to `/config/procula/notifications_feed.json` (ring buffer, 50 events), bell icon with unread badge, Processing section on dashboard with job cards and progress bars.

**Phase D — Request Queue:** First-party viewer request queue built into the dashboard. Viewers request from search; admins approve/deny with configurable Radarr/Sonarr quality profiles. Apprise notifies on state change. Import webhook auto-transitions requests to "available".

**Phase E — Transcoding:** `procula/process.go` with FFmpeg progress tracking (parses `time=` from stderr), profile matching on codec or resolution, two default profiles shipped disabled (`compatibility-h264.json`, `mobile-1080p.json`).

**Phase F — External Notifications (Apprise):** Apprise container (opt-in Docker Compose profile), `direct` mode for single-webhook setups (ntfy, Gotify, any webhook URL), config at `/config/procula/notifications.json`. Discord is not a supported provider.

**Invite Flow (Phase D follow-up):** One-time shareable invite links for admin-free user onboarding. `POST /api/pelicula/invites` generates a 32-byte random base64url token (stored in `/config/pelicula/invites.json`) with configurable TTL (default 7 days) and optional max-uses cap. Admins create links via the dashboard Users section ("Create invite link" button). `/register` (static HTML, no Pelicula session required) renders a username+password form that submits to `POST /api/pelicula/invites/{token}/redeem`, creating the Jellyfin account on success. Expired, exhausted, and revoked tokens return clear error states. Admin can revoke or delete tokens from the dashboard.

**Bazarr — Subtitle Acquisition:** Bazarr container wired into the stack alongside Sonarr/Radarr. Auto-wire creates a language profile from `PELICULA_SUB_LANGS` (set via `./pelicula configure` → Subtitles) and connects Bazarr to both *arr apps on startup. nginx proxies `/bazarr`. Procula flags imports that are missing subtitles for the configured languages; Bazarr handles acquisition via its own Sonarr/Radarr polling — Procula does not talk to Bazarr directly.

**Dual Subtitles:** Procula pipeline stage that generates stacked ASS sidecar files (`Movie.en-es.ass`) alongside source media. Base language (familiar) appears bottom-center in white; secondary (learning) appears top-center in yellow. Source cues are extracted from embedded subtitle streams or `.{lang}.srt` / `.{lang}.ass` sidecars; Argos Translate (offline, not bundled in image) synthesizes missing tracks. Cue alignment is base-anchored (secondary cue midpoint must fall within base cue range). Configurable via env vars (`DUALSUB_ENABLED`, `DUALSUB_PAIRS`, `DUALSUB_TRANSLATOR`) and Procula settings UI. Known limitations: no bitmap (PGS/DVD) sub support; font fallback required for Arial; per-title opt-out not yet implemented.
