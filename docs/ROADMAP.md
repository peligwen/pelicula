# Pelicula Roadmap

Pelicula's core phases (A–F) are shipped. This file tracks what's next, what's deferred, and summarises what landed.

---

## Active

---

## Peligrosa Initiative

Security and user-interaction safety hardening. See [PELIGROSA.md](PELIGROSA.md) for the full threat model and current surface.

- [x] **[Peligrosa] Central CSRF middleware** — `requireLocalOriginStrict` / `requireLocalOriginSoft` wired per-route in `main.go`, replacing 8 inline checks across 5 files.
- [x] **[Peligrosa] Jellyfin auth** — credentials verified against Jellyfin's `/Users/AuthenticateByName`; roles stored in `roles.json`; Jellyfin admins auto-promoted. `password` and `users` modes removed. Legacy `off` mode removed (Tasks 1–11).
- [x] **[Peligrosa] `PELICULA_AUTH=off` removal** — auth is now always on. A narrowly-scoped loopback auto-session grants admin access to requests from the host machine (docker upstream CIDR + loopback `X-Real-IP` + loopback `Host`). Existing installs: no action needed — the next restart picks up the change.
- [x] **[Peligrosa] `middleware/peligrosa/` subpackage** — auth, invites, requests, user CRUD, and webhook validation extracted into a Go subpackage with an explicit API surface (`peligrosa.RegisterRoutes`).
- [x] **[Peligrosa] Hardened X-Real-IP trust (MEDIUM-3)** — `ClientIP` now honors `X-Real-IP` only when the socket peer is within the trusted upstream CIDR. Rate-limit bypass via header spoofing is closed.
- [x] **[Peligrosa] Remote role capping** — defense-in-depth: the remote nginx vhost injects `X-Pelicula-Remote: true`; middleware caps effective role to `viewer` regardless of stored role. Prevents credential escalation via the remote vhost.
- [x] **[Peligrosa] Open LAN registration** — optional `PELICULA_OPEN_REGISTRATION` setting: `/register` without a token creates a Jellyfin viewer account. LAN-only, rate-limited, always viewer role.
- [x] **[Peligrosa] First-admin password** — setup wizard generates `JELLYFIN_PASSWORD` and prints admin credentials after ``pelicula up``.
- [x] **[Peligrosa] HMAC invite tokens** — closed. 256-bit random token + SQLite lookup is already secure; HMAC signing adds no practical security and can't eliminate DB lookups (revocation still requires one).
- [ ] **[Peligrosa] Plex SSO** — deferred; different API shape (plex.tv OAuth dance).

---

## Deferred

- **Invite Apprise notification**: notify admin via Apprise/internal feed when an invite is claimed. Deferred; low priority since the dashboard shows active invites and redemption history.

- **Plex SSO**: moved to Peligrosa initiative above (Jellyfin SSO shipped).
- **Jellyfin as optional service**: acquisition-only mode for users who have their own media server (Plex, Emby, external Jellyfin). Jellyfin stays always-on until this is needed.
- **Retire/retention/storage pruning**: storage management and dedup reporting. Deferred, no timeline.
- **NFS-backed library (named volumes)**: host `movies/` and `tv/` on a NAS via NFS without a macOS Finder mount. Docker Desktop's Linux VM mounts the export directly through `local` volumes with `driver_opts: type=nfs`, so containers read/write it as normal named volumes — no `/Volumes`, no VirtioFS, no FUSE. Keep `WORK_DIR` (downloads + processing) local because NFS breaks hardlinks and is poorly suited to active torrent I/O; accept that Sonarr/Radarr will fall back to copy-on-import. Shape: new `compose/docker-compose.nfs.yml` + `compose/docker-compose.local-library.yml` override pair; `LIBRARY_NFS` / `NFS_HOST` / `NFS_EXPORT` / `NFS_OPTIONS` in `.env`; ``pelicula up`` picks the right overlay. Full plan: `~/.claude/plans/shiny-floating-cosmos.md`.

---

## Shipped

**Dashboard Consolidation:** Retired the standalone Procula UI (`/procula/`) — all pipeline visibility is now in the main dashboard. Services grid removed in favour of sidebar-only services list. New Settings tab centralises pipeline toggles, subtitle settings, notification mode, and download defaults. Search cards are click-to-expand with detail chips (rating, certification, runtime, network, genres). New "monitoring" pipeline lane shows grabbed requests that haven't started downloading yet. In-page job drawer replaces the Procula link (shows validation checks, file info, transcode details, timeline). Event log with filter chips and pagination. Password auto-generated on the register page with visibility toggle.

**Infrastructure:** Prowlarr now runs on gluetun's network namespace (alongside qBittorrent) so all indexer traffic is VPN-routed. Prowlarr is reachable at `gluetun:9696`; nginx updated accordingly.

**CLI additions:** `pelicula redeploy [svc]` rebuilds Docker images then does a full stack down/up (distinct from `rebuild` which only restarts). After `pelicula up`, the CLI polls for first-run state and auto-opens the registration page in the browser if no admin is registered yet.

**Pre-v1.0 Hardening:** SQLite data layer for all mutable state (`modernc.org/sqlite` via pure-Go driver). Migration framework with `PRAGMA user_version` for both middleware and procula. Auto-migration from JSON files on first startup (idempotent, handles corrupt files). Configurable service URLs via environment variables (`SONARR_URL`, `RADARR_URL`, etc.). Versioned backup format (v1→v2) with forward-compatible import chain; v2 includes roles, invites, and requests. Go CLI rewrite (`cmd/pelicula/`) — single binary, cross-platform (macOS/Linux/Windows/Synology), stdlib-only, replaces the bash script. API contract freeze with stability policy (additive-only changes).

**Phase A — Onboarding:** Two-prompt setup (VPN key + country), `--advanced` walkthrough, `the Settings UI` runtime menu, `set_env_var` helper, `$CONFIG_DIR/pelicula/` directory.

**Phase B — Auth & Roles:** Jellyfin-backed auth with viewer / manager / admin roles, `Guard` / `GuardManager` / `GuardAdmin` middleware, dashboard login form, role-based UI hiding. Post-ship hardening: CSRF origin check, `MaxBytesReader`, username and UUID validation. Legacy `off` mode later removed in the Peligrosa extraction (Tasks 1–11).

**Phase C — In-Dashboard Notifications:** Procula catalog stage writes to `/config/procula/notifications_feed.json` (ring buffer, 50 events), bell icon with unread badge, Processing section on dashboard with job cards and progress bars.

**Phase D — Request Queue:** First-party viewer request queue built into the dashboard. Viewers request from search; admins approve/deny with configurable Radarr/Sonarr quality profiles. Apprise notifies on state change. Import webhook auto-transitions requests to "available".

**Phase E — Transcoding:** `procula/process.go` with FFmpeg progress tracking (parses `time=` from stderr), profile matching on codec or resolution, two default profiles shipped disabled (`compatibility-h264.json`, `mobile-1080p.json`).

**Phase F — External Notifications (Apprise):** Apprise container (opt-in Docker Compose profile), `direct` mode for single-webhook setups (ntfy, Gotify, any webhook URL), config at `/config/procula/notifications.json`. Discord is not a supported provider.

**Invite Flow (Phase D follow-up):** One-time shareable invite links for admin-free user onboarding. `POST /api/pelicula/invites` generates a 32-byte random base64url token (stored in `/config/pelicula/invites.json`) with configurable TTL (default 7 days) and optional max-uses cap. Admins create links via the dashboard Users section ("Create invite link" button). `/register` (static HTML, no Pelicula session required) renders a username+password form that submits to `POST /api/pelicula/invites/{token}/redeem`, creating the Jellyfin account on success. Expired, exhausted, and revoked tokens return clear error states. Admin can revoke or delete tokens from the dashboard.

**Bazarr — Subtitle Acquisition:** Bazarr container wired into the stack alongside Sonarr/Radarr. Auto-wire creates a language profile from `PELICULA_SUB_LANGS` (set via `the Settings UI` → Subtitles) and connects Bazarr to both *arr apps on startup. nginx proxies `/bazarr`. Procula flags imports with missing subtitles, then actively kicks Bazarr via its API to search immediately (rather than waiting for Bazarr's background polling cycle), waits up to 30 minutes for sidecars to appear, and proceeds to dual-sub generation once they arrive.

**Dual Subtitles:** Procula pipeline stage that generates stacked ASS sidecar files (`Movie.en-es.ass`) alongside source media. Base language (familiar) appears bottom-center in white; secondary (learning) appears top-center in yellow. Source cues are extracted from embedded subtitle streams or `.{lang}.srt` / `.{lang}.ass` sidecars; Argos Translate (offline, not bundled in image) synthesizes missing tracks. Cue alignment is base-anchored (secondary cue midpoint must fall within base cue range). Configurable via env vars (`DUALSUB_ENABLED`, `DUALSUB_PAIRS`, `DUALSUB_TRANSLATOR`) and Procula settings UI. Known limitations: no bitmap (PGS/DVD) sub support; font fallback required for Arial; per-title opt-out not yet implemented.
