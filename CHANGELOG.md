# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

---

## [Unreleased]

### Changed
- **Network dashboard drawer** — replaced the packet-capture-based connections list with a per-container bandwidth panel (Container / In / Out / Route). Backed by Docker stats through the existing docker-socket-proxy; no new container privileges. VPN-routed containers are flagged via a static membership list.
- **Webhook secret delivery** — `WEBHOOK_SECRET` is now sent via the `X-Webhook-Secret` request header instead of a `?secret=` URL query parameter. The header is injected by `wireImportWebhook()` when auto-wiring Radarr/Sonarr; the middleware validates it with `crypto/subtle.ConstantTimeCompare`. Existing installs without `WEBHOOK_SECRET` set continue to work (check is skipped when env var is unset).
- **Build version in logs** (R8 P1 / R16 P5) — deployed binaries now log a real `git describe` version string via build-time ldflags (`-X main.Version`). The `pelicula` bash wrapper, the middleware Dockerfile, and the procula Dockerfile all pass `--build-arg VERSION=$(git describe)`. Fresh-clone `./pelicula --version` now shows the real version instead of `"dev"`.
- **Bazarr default timeout** (R9) — bumped from 10 s to 30 s to avoid spurious failures under subtitle-search load.

### Added
- **nginx auth rate limits** (R15 P2) — `peli_auth` limit_req zone (10 m shared memory, 10 r/min) applied at the nginx edge to `/auth/login` (burst=5), `/register` (burst=3), `/invites/{token}/{check,redeem}` (burst=5), and `/generate-password` (burst=5). Brute-force bursts are shed before reaching Go; legitimate users stay well under the threshold. All rate-limited endpoints return 429 (previously 503) on rejection. `client_max_body_size 8k` caps oversized POST payloads on each zone.
- **VPN degraded/recovered Apprise notifications** (R12 P4) — `vpnwatchdog` now fires an Apprise notification on exactly two transitions: when the VPN enters a degraded state and when it recovers. Previously VPN drops surfaced only as `slog.Warn` lines in the journal.

### Removed
- **Pelicula-managed remote-access features** — Pelicula no longer orchestrates an external nginx vhost, Let's Encrypt / certbot, REMOTE_MODE settings, or the cloudflared / tailscale auto-wiring overlays. Pelicula listens LAN-only on port 7354; Jellyfin's built-in HTTPS on port 8920 is the supported external surface (operator port-forwards via DSM / router). Retires `nginx/remote.conf.template`, `nginx/remote-simple.conf.template`, `compose/docker-compose.cloudflared.yml`, `compose/docker-compose.tailscale.yml`, `cmd/pelicula/remote.go`, the certbot service, and the Settings → Remote access UI. Existing `.env` files have `REMOTE_*`, `CLOUDFLARE_TUNNEL_TOKEN`, and `TAILSCALE_*` keys auto-stripped on next `pelicula up` (cert dirs are also cleaned up). For invitee dashboard remote access, install Tailscale directly on the host — see docs/PELIGROSA.md.
- **`netcap` sidecar** — the raw-packet-capture container (`NET_ADMIN`/`NET_RAW`) and its `127.0.0.1:2375` host-gateway plumbing are gone. The dashboard's network view no longer shows individual connections or destination hosts; bandwidth totals replace them. The `/api/pelicula/network` endpoint keeps its path but returns a new shape (see API.md).

### Fixed
- **Missing-watcher cooldown reset** (R10 P2) — the per-item cooldown is now reset on every successful import webhook, so a newly-available title is re-queued for the next scan cycle instead of waiting out the original backoff. Eliminates the most common "title grabbed but never became available in Jellyfin" report.
- **Catalog Upsert atomicity** (R7 P3) — `Upsert` is now safe under concurrent callers (poller, backfill goroutine, webhook). The previous non-atomic path could produce duplicate catalog rows visible on the dashboard.
- **Stack-restart response flush** (closes #6) — `HandleStackRestart` now calls `http.Flusher.Flush()` before launching the self-restart goroutine, eliminating a race where the response might not reach the client before the process exits.
- **Admin API nginx rate limit** (closes #6) — `/api/pelicula/admin/` routes are now behind a dedicated `limit_req` zone (30 req/min, burst 10) at the nginx layer.
- **ffprobe context propagation** (closes #8) — `probeSubStreams` and `runFFprobe` now accept `context.Context`; HTTP handler deadlines and job cancellations propagate to the ffprobe subprocess, preventing indefinite hangs on stalled media files.
- **Dualsub warning messages** (closes #9) — fixed `<nil>` appearing in dualsub warning messages when a subtitle track was present but empty. Messages now distinguish "load error" from "no cues found".
- **Dualsub JSON sentinel leak** (closes #9) — `TrackPair` no longer emits `top_sub_index: -1` / `bottom_sub_index: -1` in JSON when using sidecar files; fields are now `*int` with omitempty.
- **Dualsub JS filter** (closes #9) — replaced implicit `undefined >= 0` coercion with explicit `'top_sub_index' in p` property checks in the browser-side pair filter.

---

## [0.1.0] — 2026-04-14

### Added
- **Bazarr** — automatic subtitle acquisition from OpenSubtitles, Addic7ed, Podnapisi, and others. Wired to Sonarr and Radarr automatically on startup. Language profile created from `PELICULA_SUB_LANGS`. Procula flags imports missing subtitles for the configured languages.
- **the dashboard Settings tab → Subtitles** — new menu option (9) to set `PELICULA_SUB_LANGS` (comma-separated ISO 639-1 codes, e.g. `en,es`). Drives both the Bazarr language profile and the Procula missing-subs check.
- **Dual subtitles** (`procula`) — new pipeline stage that generates stacked ASS sidecar files (e.g. `Movie.en-es.ass`) for language learners. Base language appears bottom-center in white; learning language appears top-center in yellow. Configure via `DUALSUB_ENABLED` / `DUALSUB_PAIRS` / `DUALSUB_TRANSLATOR` or the Procula settings UI. Argos Translate (offline) is supported as a fallback translator when no secondary track exists. Known limitations: no bitmap (PGS/DVD) sub support; per-title opt-out not yet implemented.
- **Invite flow** — shareable one-time invite links for admin-free user onboarding. Admins create links from the dashboard Users section; recipients choose a username and password at `/register`. Configurable TTL (default 7 days) and max-uses cap. Expired/exhausted/revoked tokens return clear error states.
- **Jellyfin SSO** — credentials verified against Jellyfin's `/Users/AuthenticateByName`; roles stored in `pelicula.db` (SQLite); Jellyfin admins auto-promoted. Existing `roles.json` and `invites.json` are auto-migrated to SQLite on first startup (renamed to `.json.migrated`).
- **Central CSRF middleware** — `requireLocalOriginStrict` / `requireLocalOriginSoft` wired per-route, replacing per-handler inline checks.
- **Removed `PELICULA_AUTH=off` mode.** Auth is now always on. A narrowly-scoped loopback auto-session grants admin access to requests from the host machine (docker upstream CIDR + loopback `X-Real-IP` + loopback `Host`). Existing installs: no action needed — the next restart picks up the change.
- **Hardened `X-Real-IP` trust (MEDIUM-3).** `ClientIP` now honors the header only when the socket peer is within the trusted upstream CIDR. Rate-limit bypass via `X-Real-IP` spoofing is closed.
- **Extracted `middleware/peligrosa/` subpackage.** Auth, invites, requests, and webhook validation now live behind an explicit API surface (`peligrosa.RegisterRoutes`).
