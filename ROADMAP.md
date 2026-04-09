# Pelicula Roadmap

Pelicula's core phases (A–F) are shipped. This file tracks what's next, what's deferred, and summarises what landed.

---

## Active

### Peligrosa Hardening

Remaining security items from the Peligrosa initiative:

- [ ] **HMAC invite tokens** — sign tokens with a server secret so validity is verifiable without a DB lookup
- [ ] **`middleware/peligrosa/` subpackage** — extract auth, invites, requests, user CRUD into a Go subpackage with explicit API surface

---

## Peligrosa Initiative

Security and user-interaction safety hardening. See [PELIGROSA.md](PELIGROSA.md) for the full threat model and current surface.

- [ ] **[Peligrosa] HMAC invite tokens** — sign tokens with a server secret so validity is verifiable without a DB lookup. Prevents brute-force token enumeration.
- [x] **[Peligrosa] Central CSRF middleware** — `requireLocalOriginStrict` / `requireLocalOriginSoft` wired per-route in `main.go`, replacing 8 inline checks across 5 files.
- [ ] **[Peligrosa] `middleware/peligrosa/` subpackage** — extract auth, invites, requests, user CRUD, and webhook validation into a Go subpackage with an explicit API surface. Blocked on `JellyfinClient` interface, `Fulfiller` injection, and shared HTTP helpers extraction. See plan file for incremental path.
- [x] **[Peligrosa] Jellyfin auth** — `PELICULA_AUTH=jellyfin` (now the only auth mode): credentials verified against Jellyfin's `/Users/AuthenticateByName`; roles stored in `roles.json`; Jellyfin admins auto-promoted. `password` and `users` modes removed.
- [x] **[Peligrosa] Remote role capping** — defense-in-depth: the remote nginx vhost injects `X-Pelicula-Remote: true`; middleware caps effective role to `viewer` regardless of stored role. Prevents credential escalation via the remote vhost.
- [x] **[Peligrosa] Open LAN registration** — optional `PELICULA_OPEN_REGISTRATION` setting: `/register` without a token creates a Jellyfin viewer account. LAN-only, rate-limited, always viewer role.
- [x] **[Peligrosa] First-admin password** — setup wizard generates `JELLYFIN_PASSWORD` and prints admin credentials after ``pelicula up``.
- [ ] **[Peligrosa] Plex SSO** — deferred; different API shape (plex.tv OAuth dance).

---

## Deferred

- **Invite Apprise notification**: notify admin via Apprise/internal feed when an invite is claimed. Deferred; low priority since the dashboard shows active invites and redemption history.
- **`the Settings UI` invite management**: list active invites and revoke them from the CLI menu. Deferred; the dashboard Users section covers this via the UI.

- **Plex SSO**: moved to Peligrosa initiative above (Jellyfin SSO shipped).
- **Jellyfin as optional service**: acquisition-only mode for users who have their own media server (Plex, Emby, external Jellyfin). Jellyfin stays always-on until this is needed.
- **Retire/retention/storage pruning**: storage management and dedup reporting. Deferred, no timeline.
- **NFS-backed library (named volumes)**: host `movies/` and `tv/` on a NAS via NFS without a macOS Finder mount. Docker Desktop's Linux VM mounts the export directly through `local` volumes with `driver_opts: type=nfs`, so containers read/write it as normal named volumes — no `/Volumes`, no VirtioFS, no FUSE. Keep `WORK_DIR` (downloads + processing) local because NFS breaks hardlinks and is poorly suited to active torrent I/O; accept that Sonarr/Radarr will fall back to copy-on-import. Shape: new `docker-compose.nfs.yml` + `docker-compose.local-library.yml` override pair; `LIBRARY_NFS` / `NFS_HOST` / `NFS_EXPORT` / `NFS_OPTIONS` in `.env`; ``pelicula up`` picks the right overlay. Full plan: `~/.claude/plans/shiny-floating-cosmos.md`.

---

## Shipped

**Pre-v1.0 Hardening:** SQLite data layer for all mutable state (`modernc.org/sqlite` via pure-Go driver). Migration framework with `PRAGMA user_version` for both middleware and procula. Auto-migration from JSON files on first startup (idempotent, handles corrupt files). Configurable service URLs via environment variables (`SONARR_URL`, `RADARR_URL`, etc.). Versioned backup format (v1→v2) with forward-compatible import chain; v2 includes roles, invites, and requests. Go CLI rewrite (`cmd/pelicula/`) — single binary, cross-platform (macOS/Linux/Windows/Synology), stdlib-only, replaces the bash script. API contract freeze with stability policy (additive-only changes).

**Phase A — Onboarding:** Two-prompt setup (VPN key + country), `--advanced` walkthrough, `the Settings UI` runtime menu, `set_env_var` helper, `$CONFIG_DIR/pelicula/` directory.

**Phase B — Auth & Roles:** Jellyfin-backed auth with viewer / manager / admin roles, `Guard` / `GuardManager` / `GuardAdmin` middleware, dashboard login form, role-based UI hiding. Post-ship hardening: `IsOffMode()` guard on `handleUsers`, CSRF origin check, `MaxBytesReader`, username and UUID validation.

**Phase C — In-Dashboard Notifications:** Procula catalog stage writes to `/config/procula/notifications_feed.json` (ring buffer, 50 events), bell icon with unread badge, Processing section on dashboard with job cards and progress bars.

**Phase D — Request Queue:** First-party viewer request queue built into the dashboard. Viewers request from search; admins approve/deny with configurable Radarr/Sonarr quality profiles. Apprise notifies on state change. Import webhook auto-transitions requests to "available".

**Phase E — Transcoding:** `procula/process.go` with FFmpeg progress tracking (parses `time=` from stderr), profile matching on codec or resolution, two default profiles shipped disabled (`compatibility-h264.json`, `mobile-1080p.json`).

**Phase F — External Notifications (Apprise):** Apprise container (opt-in Docker Compose profile), `direct` mode for single-webhook setups (ntfy, Gotify, any webhook URL), config at `/config/procula/notifications.json`. Discord is not a supported provider.

**Invite Flow (Phase D follow-up):** One-time shareable invite links for admin-free user onboarding. `POST /api/pelicula/invites` generates a 32-byte random base64url token (stored in `/config/pelicula/invites.json`) with configurable TTL (default 7 days) and optional max-uses cap. Admins create links via the dashboard Users section ("Create invite link" button). `/register` (static HTML, no Pelicula session required) renders a username+password form that submits to `POST /api/pelicula/invites/{token}/redeem`, creating the Jellyfin account on success. Expired, exhausted, and revoked tokens return clear error states. Admin can revoke or delete tokens from the dashboard.

**Bazarr — Subtitle Acquisition:** Bazarr container wired into the stack alongside Sonarr/Radarr. Auto-wire creates a language profile from `PELICULA_SUB_LANGS` (set via `the Settings UI` → Subtitles) and connects Bazarr to both *arr apps on startup. nginx proxies `/bazarr`. Procula flags imports that are missing subtitles for the configured languages; Bazarr handles acquisition via its own Sonarr/Radarr polling — Procula does not talk to Bazarr directly.

**Dual Subtitles:** Procula pipeline stage that generates stacked ASS sidecar files (`Movie.en-es.ass`) alongside source media. Base language (familiar) appears bottom-center in white; secondary (learning) appears top-center in yellow. Source cues are extracted from embedded subtitle streams or `.{lang}.srt` / `.{lang}.ass` sidecars; Argos Translate (offline, not bundled in image) synthesizes missing tracks. Cue alignment is base-anchored (secondary cue midpoint must fall within base cue range). Configurable via env vars (`DUALSUB_ENABLED`, `DUALSUB_PAIRS`, `DUALSUB_TRANSLATOR`) and Procula settings UI. Known limitations: no bitmap (PGS/DVD) sub support; font fallback required for Arial; per-title opt-out not yet implemented.
