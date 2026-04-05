# Pelicula Roadmap

Pelicula is evolving from a single-admin media stack into a multi-user product. This document tracks planned phases and their implementation status.

---

## Phase A — Onboarding

Reduce friction on first run. Add runtime configuration menu.

- [x] Simplify `./pelicula setup` to 2 required prompts (VPN key + country); auto-detect everything else
- [x] Add `./pelicula setup --advanced` for full first-run walkthrough (TZ, PUID/PGID, paths, port, auth)
- [x] Add `./pelicula configure` runtime configuration menu (auth, notifications, transcoding, network, paths)
- [x] Add `set_env_var` helper for idempotent `.env` updates
- [x] Create `$CONFIG_DIR/pelicula/` directory for future user data

---

## Phase B — Auth & Roles

Multi-user access to the dashboard with role-based visibility.

- [x] Pelicula user model: `users.json` at `/config/pelicula/users.json`
- [x] Three roles: `viewer` (read-only + Jellyseerr requests), `manager` (search + add + pause/resume), `admin` (everything)
- [x] Rewrite `middleware/auth.go`: session stores username + role; `Guard` checks role per endpoint
- [x] `PELICULA_AUTH=users` mode in `middleware/main.go`
- [x] Dashboard login form: username + password fields (users mode shows username field)
- [x] Dashboard hides destructive controls (cancel, blocklist) based on role
- [x] `./pelicula configure` → Auth section: create/edit/delete users

**Security boundaries:**
- Destructive actions (cancel + delete files, blocklist) → admin only
- Additive actions (search + add content) → manager+
- *arr UIs (Sonarr/Radarr/Prowlarr/qBittorrent) → admin only

---

## Phase C — In-Dashboard Notifications

Zero-config "content ready" signal. No external services required.

- [x] Implement `procula/catalog.go`: Jellyfin library refresh + notification event on completion
- [x] Write notification events to `/config/procula/notifications_feed.json` (ring buffer, 50 events)
- [x] New middleware endpoint `GET /api/pelicula/notifications` proxies Procula feed
- [x] New middleware endpoint `POST /api/pelicula/jellyfin/refresh` (internal, Procula calls this)
- [x] Dashboard: bell icon in masthead with unread count badge
- [x] Dashboard: notification dropdown with recent events (localStorage tracks last-seen)
- [x] Dashboard: Processing section between Downloads and Services (job cards, progress bars, stage badges)

**Events:** content ready, validation failed (blocklisted + re-searching), transcoding complete, storage warning

---

## Phase D — Jellyseerr

Multi-user request management. Dashboard search wraps Jellyseerr API.

- [ ] Add `jellyseerr` service to `docker-compose.yml` (Docker Compose profile, opt-in)
- [ ] Add nginx proxy at `/jellyseerr`
- [ ] Implement `wireJellyseerr` in `middleware/autowire.go`: connect to Jellyfin auth backend, add Radarr+Sonarr
- [ ] `middleware/search.go`: when `JELLYSEERR_ENABLED=true`, route add requests through Jellyseerr's `/api/v1/request`; fall back to direct *arr calls when disabled
- [ ] Add Jellyseerr to `middleware/services.go` health checks
- [ ] Add Jellyseerr card to dashboard services grid
- [ ] `./pelicula configure` → Auth section: enable/disable Jellyseerr

---

## Phase E — Transcoding

Available but dormant by default. Only runs when a matching profile is enabled.

- [ ] Implement `procula/process.go`: FFmpeg invocation with progress tracking (parse `frame=` / `time=` / `speed=` from stderr)
- [ ] New `procula/profiles.go`: profile CRUD API, load profiles from `/config/procula/profiles/`
- [ ] Ship two default profile templates (disabled by default):
  - `compatibility-h264.json` — HEVC/AV1 → H.264 for max device compatibility
  - `mobile-1080p.json` — 4K → 1080p with stereo audio
- [ ] `procula/pipeline.go`: wire process stage (validate → process → catalog)
- [ ] `./pelicula configure` → Transcoding section: enable/disable, list profiles

---

## Phase F — External Notifications (Apprise)

Push notifications to phone, email, Telegram, etc.

- [ ] Add `apprise` service to `docker-compose.yml` (Docker Compose profile, opt-in)
- [ ] Extend `procula/catalog.go`: POST to `http://apprise:8000/notify` when configured
- [ ] `direct` mode: single HTTP POST without Apprise container (ntfy / Gotify / any webhook URL)
- [ ] Config in `/config/procula/notifications.json`: mode, apprise_urls, direct_url
- [ ] `./pelicula configure` → Notifications section: choose provider, enter credentials, test

**Providers via Apprise:** ntfy, Gotify, email/SMTP, Pushover, Telegram, and 85+ others. Discord is not a supported option.

---

## Deferred

- **Jellyfin/Plex SSO**: layer on top of the Phase B user model. Delegates auth to Jellyfin or Plex; Pelicula user model is the standalone fallback.
- **Jellyfin as optional service**: acquisition-only mode for users who have their own media server (Plex, Emby, external Jellyfin). Jellyfin stays always-on until this is needed.
- **Retire/retention/storage pruning**: storage management and dedup reporting. Deferred, no timeline.
