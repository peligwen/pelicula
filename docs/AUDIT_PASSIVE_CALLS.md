# Passive Outbound Call Audit

## Overview

This document catalogs every outbound network call the pelicula stack makes **without a direct user HTTP action against that specific resource** — background pollers, watchdogs, boot-time probes, third-party telemetry, and scheduled tasks.

The audit exists to:
- Make the full network footprint visible and reviewable
- Identify calls that hit the public internet unexpectedly
- Serve as a gate for new passive calls (see contributor checklist)

**"Passive"** means the call fires on a timer, at boot, or as a side-effect of unrelated user activity — not as the direct response to a user requesting that specific data. A user loading the dashboard while a poller fires is passive; a user clicking "search for missing episodes" is not.

### Impact classes

| Class | Definition | Concern level |
|---|---|---|
| `external-public` | Hits the public internet | Highest — privacy, data leakage, dependency on external uptime |
| `cross-service` | Hits another container on the compose network | Low external impact; costs CPU/memory and can mask service misbehavior |
| `intra-container` | Internal to a single container (SQLite, localhost) | No concern |

---

## Inventory

### middleware (pelicula-api) — boot probes

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `autowire/autowire.go:218` | `GET {sonarr}/ping` | Boot | Every 3s, up to 120s | `cross-service` | ✅ accepted |
| `autowire/autowire.go:218` | `GET {radarr}/ping` | Boot | Every 3s, up to 120s | `cross-service` | ✅ accepted |
| `autowire/autowire.go:218` | `GET {jellyfin}/System/Info/Public` | Boot | Every 3s, up to 120s | `cross-service` | ✅ accepted |
| `autowire/autowire.go:218` | `GET {bazarr}/` | Boot | Every 3s, up to 120s | `cross-service` | ✅ accepted |
| `autowire/autowire.go:218` | `GET {prowlarr}/ping` | Boot (VPN profile) | Every 3s, up to 120s | `cross-service` | ✅ accepted |
| `autowire/autowire.go:218` | `GET {qbt}/` | Boot (VPN profile) | Every 3s, up to 120s | `cross-service` | ✅ accepted |

### middleware (pelicula-api) — auto-wiring (one-shot post-boot)

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `autowire/autowire.go` | Arr downloadclient, rootfolder, notification, Prowlarr app config | Post-boot, services ready | Once | `cross-service` | ✅ accepted |
| `autowire/autowire.go` | `POST {sonarr}/api/v3/command CheckHealth` | Post-boot | Once | `cross-service` | ✅ accepted |
| `autowire/autowire.go` | `POST {radarr}/api/v3/command CheckHealth` | Post-boot | Once | `cross-service` | ✅ accepted |
| `autowire/autowire.go:544-592` | `GET {bazarr}/api/system/settings` | Post-boot | Once | `cross-service` | ✅ accepted |
| `autowire/autowire.go:544-592` | `GET {bazarr}/api/system/languages/profiles` | Post-boot | Once | `cross-service` | ✅ accepted |
| `autowire/autowire.go:544-592` | `POST {bazarr}/api/system/settings` | Post-boot | Once | `cross-service` | ✅ accepted |

### middleware (pelicula-api) — SSE poller

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `sse/poller.go:217` | `GET {qbt}/api/v2/torrents/info` | SSE client connected | Every 5s | `cross-service` | 🔧 tuned — paused when no clients |
| `sse/poller.go:228` | `GET {qbt}/api/v2/transfer/info` | SSE client connected | Every 5s | `cross-service` | 🔧 tuned — paused when no clients |
| `services/clients.go:366` | `GET {sonarr}/ping` | SSE client connected | Every 5s (2s timeout) | `cross-service` | 🔧 tuned — paused when no clients |
| `services/clients.go:366` | `GET {radarr}/ping` | SSE client connected | Every 5s (2s timeout) | `cross-service` | 🔧 tuned — paused when no clients |
| `services/clients.go:366` | `GET {prowlarr}/ping` | SSE client connected | Every 5s (2s timeout) | `cross-service` | 🔧 tuned — paused when no clients |
| `services/clients.go:366` | `GET {qbt}/` | SSE client connected | Every 5s (2s timeout) | `cross-service` | 🔧 tuned — paused when no clients |
| `services/clients.go:366` | `GET {jellyfin}/health` | SSE client connected | Every 5s (2s timeout) | `cross-service` | 🔧 tuned — paused when no clients |
| `services/clients.go:366` | `GET {bazarr}/` | SSE client connected | Every 5s (2s timeout) | `cross-service` | 🔧 tuned — paused when no clients |

### middleware (pelicula-api) — catalog queue poller

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `catalog/poller.go:39` | `GET {radarr}/api/v3/queue` (paginated, full) | Always-on ticker | Every 60s, no jitter | `cross-service` | ✅ accepted |
| `catalog/poller.go:50` | `GET {sonarr}/api/v3/queue` (paginated, full) | Always-on ticker | Every 60s, no jitter | `cross-service` | ✅ accepted |

### middleware (pelicula-api) — missing-content watcher

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `missingwatcher/watcher.go:91` | `GET {radarr}/api/v3/movie` (full list) | Ticker | Every 2min → 🔧 10min, no jitter | `cross-service` | 🔧 tuned — interval extended |
| `missingwatcher/watcher.go` | `GET {radarr}/api/v3/queue` (paginated) | Ticker | Every 2min → 🔧 10min | `cross-service` | 🔧 tuned — interval extended |
| `missingwatcher/watcher.go:105` | `POST {radarr}/api/v3/command MoviesSearch` | Missing items found | Per-cycle when missing | `cross-service` | ✅ accepted — per-item backoff: 30min/2h/12h/24h |
| `missingwatcher/watcher.go:144` | `GET {sonarr}/api/v3/wanted/missing?pageSize=100` | Ticker | Every 2min → 🔧 10min | `cross-service` | 🔧 tuned — interval extended |
| `missingwatcher/watcher.go` | `GET {sonarr}/api/v3/queue` (paginated) | Ticker | Every 2min → 🔧 10min | `cross-service` | 🔧 tuned — interval extended |
| `missingwatcher/watcher.go:174` | `POST {sonarr}/api/v3/command EpisodeSearch` | Missing items found | Per-cycle when missing | `cross-service` | ✅ accepted — per-item backoff: 30min/2h/12h/24h |

### middleware (pelicula-api) — VPN watchdog

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `vpnwatchdog/watchdog.go:21` | `GET {gluetun}/v1/openvpn/portforwarded` | Always-on ticker | Every 30s | `cross-service` | ✅ accepted — see Known Exceptions |
| `vpnwatchdog/watchdog.go` | `GET {gluetun}/v1/openvpn/status` | Port == 0 | On condition | `cross-service` | ✅ accepted |
| `vpnwatchdog/watchdog.go` | `POST {qbt}/api/v2/app/setPreferences` | Port change detected | On event | `cross-service` | ✅ accepted |

### middleware (pelicula-api) — dashboard page loads (effectively passive for open tabs)

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `catalog/handler.go:52` | `GET {radarr}/api/v3/movie` (full list) | `/api/pelicula/catalog` request | Per page load, no cache | `cross-service` | ⚠️ flag — no cache |
| `catalog/handler.go:52` | `GET {sonarr}/api/v3/series` (full list) | `/api/pelicula/catalog` request | Per page load, no cache | `cross-service` | ⚠️ flag — no cache |
| `app.go:69` | `GET {prowlarr}/api/v1/indexer` | `/api/pelicula/status` request | Per request, 5min TTL cache | `cross-service` | ✅ accepted |
| `app.go:172` | CheckHealth (6 services) | `/api/pelicula/status` request | Per request, 5s TTL cache | `cross-service` | ✅ accepted |
| `hooks/notif.go:28` | `GET {radarr}/api/v3/history?pageSize=20` | `/api/pelicula/hooks/notifications` | Per request | `cross-service` | ✅ accepted |
| `hooks/notif.go:28` | `GET {sonarr}/api/v3/history?pageSize=20` | `/api/pelicula/hooks/notifications` | Per request | `cross-service` | ✅ accepted |
| (catalog handler) | `GET {radarr}/api/v3/qualityprofile` | `/api/pelicula/catalog/qualityprofiles` | Per request, no cache | `cross-service` | ⚠️ flag — no cache |
| (catalog handler) | `GET {sonarr}/api/v3/qualityprofile` | `/api/pelicula/catalog/qualityprofiles` | Per request, no cache | `cross-service` | ⚠️ flag — no cache |

### procula — periodic

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `libraries.go:47` | `GET {pelicula-api}/api/pelicula/libraries` | Boot | Once, 5s timeout | `cross-service` | ✅ accepted |
| `libraries.go:86` | `GET {pelicula-api}/api/pelicula/libraries` | Ticker | Every 5min, no jitter | `cross-service` | ✅ accepted — see Known Exceptions |
| `updates.go:41` | `GET https://api.github.com/repos/peligwen/pelicula/releases/latest` | Boot + ticker | 30s after boot, then every 24h; disk-cached 24h | `external-public` | ✅ accepted — see Known Exceptions |

### procula — event-driven (per-job)

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| `catalog.go:445` | `POST {pelicula-api}/api/pelicula/jellyfin/refresh` | Successful job | Per job | `cross-service` | ✅ accepted |
| `pipeline.go:687` | `POST {pelicula-api}/api/pelicula/downloads/cancel` | Validation failure | Per failure, 3 retries (2/4/6s) | `cross-service` | ✅ accepted |
| `bazarr.go:69` | `PATCH {bazarr}/api/{movies,episodes}/subtitles` | Per language per job | Per job | `cross-service` | ✅ accepted |
| `catalog.go:408` | `POST http://apprise:8000/notify` | Successful job (opt-in) | Per job | `cross-service` | ✅ accepted — opt-in only |

### Sonarr / Radarr / Prowlarr — third-party defaults

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| Sonarr/Radarr/Prowlarr | `https://sentry.io` (crash envelopes) | Crash / unhandled error | On event; `Sentry/` dirs confirmed | `external-public` | 🔕 silenced — `AnalyticsEnabled=false` in config.xml |
| Sonarr/Radarr | RSS sync | Prowlarr feeds | ~15min (Servarr default) | `cross-service` | ✅ accepted |
| Sonarr/Radarr | Metadata refresh | Internal scheduler | Daily (Servarr default) | `cross-service` | ✅ accepted |
| Sonarr/Radarr | Wanted/missing scans | Internal scheduler | Servarr default | `cross-service` | ✅ accepted |

Note: `UpdateMechanism=Docker` is set — no auto-update calls. `LogLevel=debug` is confirmed in config.xml line 5 for each arr; verbose but no external impact.

### Bazarr — third-party defaults

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| Bazarr | Analytics endpoint | Background | Continuous | `external-public` | 🔕 silenced — `analytics.enabled: false` in config.yaml |
| Bazarr | Auto-update check | Background | Periodic | `external-public` | ⚠️ flag — `auto_update: true` in config.yaml; update mechanism is Docker, so this is redundant |
| Bazarr | Wanted subtitle search (TV) | Scheduler | Every 6h (`wanted_search_frequency`) | `external-public` (subtitle providers) | ✅ accepted |
| Bazarr | Wanted subtitle search (movies) | Scheduler | Every 6h | `external-public` (subtitle providers) | ✅ accepted |
| Bazarr | Subtitle upgrade scan | Scheduler | Every 12h (`upgrade_frequency`) | `external-public` (subtitle providers) | ✅ accepted |
| Bazarr | Subtitle search (podnapisi, yifysubtitles, opensubtitlescom) | Per-job (procula trigger) | Per job | `external-public` | ✅ accepted |

Adaptive search note: 3-week delay before searching for new content (Bazarr default).

### Jellyfin — third-party defaults

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| Jellyfin | `https://repo.jellyfin.org/files/plugin/manifest.json` | Plugin repo check | Daily (`system.xml:147`) | `external-public` | ⚠️ flag — `AllowClientLogUpload: true` also set |
| Jellyfin | Client log upload | On client log event | Event-driven | `external-public` | ⚠️ flag — `AllowClientLogUpload: true` in system.xml:162 |
| Jellyfin | External content in suggestions | On suggestions request | Per user request | `external-public` | ⚠️ flag — `EnableExternalContentInSuggestions=true` |

### Docker healthchecks

| Source | Target endpoint | Trigger | Cadence | Impact class | Status |
|---|---|---|---|---|---|
| Docker engine | `wget --spider` on 9 services | Compose runtime | Every 30s, 10s timeout (~18/min total) | `intra-container` | ✅ accepted — no external impact |

---

## Known Exceptions

These calls are accepted as-is with explicit rationale.

| Call | Cadence | Rationale |
|---|---|---|
| VPN watchdog → gluetun port check | 30s | Port forwarding leaks are silent and damaging. 30s is the minimum useful detection window for qBittorrent connectivity. Slower polling means torrents stall undetected. |
| Docker healthchecks | 30s per container | Standard compose runtime behavior; all traffic is container-local. No external network impact. |
| procula libraries refresh | 5min | Library list changes after imports or resets. 5min staleness is acceptable; shorter would be noisy. |
| GitHub update check | 24h, disk-cached 24h | Self-hosted update awareness. Single GET to a public API, result cached to disk — effectively one call per day maximum. Skips entirely on cache hit. |
| Jellyfin plugin repo manifest | Daily | Jellyfin's plugin framework requires this to display the plugin catalog in the admin UI. Disabling it breaks plugin management entirely. Acceptable given daily cadence. |

---

## Contributor Checklist

Before adding any new passive call, confirm all of the following:

- [ ] **Impact class documented** — is this `external-public`, `cross-service`, or `intra-container`? If `external-public`, justify it explicitly.
- [ ] **Jitter added** — fixed-interval pollers that fan out to multiple services should add random jitter (e.g., `±20%` of interval) to avoid thundering herd on boot.
- [ ] **Shared cache checked** — if another poller already fetches this data, consume the cache rather than adding a second caller.
- [ ] **Failure backoff** — does the call have exponential backoff or circuit-breaking on repeated failure? A tight ticker hitting a down service is a busy-loop.
- [ ] **User-Agent set** — outbound calls to external services should identify themselves (`pelicula/<version>`).
- [ ] **This file updated** — add a row to the relevant table with source file, endpoint, cadence, and status.
- [ ] **No call when idle** — if the call is only useful when users are active (e.g., SSE poller), gate it on client presence rather than running continuously.
