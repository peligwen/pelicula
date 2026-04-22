# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

---

## [Unreleased]

### Changed
- **Network dashboard drawer** — replaced the packet-capture-based connections list with a per-container bandwidth panel (Container / In / Out / Route). Backed by Docker stats through the existing docker-socket-proxy; no new container privileges. VPN-routed containers are flagged via a static membership list.
- **Webhook secret delivery** — `WEBHOOK_SECRET` is now sent via the `X-Webhook-Secret` request header instead of a `?secret=` URL query parameter. The header is injected by `wireImportWebhook()` when auto-wiring Radarr/Sonarr; the middleware validates it with `crypto/subtle.ConstantTimeCompare`. Existing installs without `WEBHOOK_SECRET` set continue to work (check is skipped when env var is unset).
- **Remote vhost CSP** — both `nginx/remote.conf.template` (full mode) and `nginx/remote-simple.conf.template` (simple mode) now emit a `Content-Security-Policy` header on all responses.

### Added
- **Simple mode remote vhost** (`nginx/remote-simple.conf.template`) — static nginx config for `REMOTE_ACCESS_ENABLED=true` without a hostname. Self-signed cert, `server_name _`, no ACME/certbot, no HSTS. TV apps and native Jellyfin clients accept self-signed certs. Enable via Settings UI → Remote access, leave hostname blank.

### Removed
- **`netcap` sidecar** — the raw-packet-capture container (`NET_ADMIN`/`NET_RAW`) and its `127.0.0.1:2375` host-gateway plumbing are gone. The dashboard's network view no longer shows individual connections or destination hosts; bandwidth totals replace them. The `/api/pelicula/network` endpoint keeps its path but returns a new shape (see API.md).

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
