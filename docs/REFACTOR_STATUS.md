# Middleware Refactor Status

This document is the authoritative retrospective for Phases 2–3 of the middleware refactor and the live tracker for Phase 4. It replaced a stale mid-Phase-2 snapshot.

---

## Retrospective

### Phase 2.1 — Package split (middleware)

Moved all handler and logic code out of the flat `cmd/pelicula-api/` structure into `internal/` subpackages. Each extraction was a discrete commit; key clusters:

- **Drift cleanup:** Deleted parallel `cmd/catalog_db.go` and `cmd/sse.go`/`cmd/sse_poller.go`; migrated tests to the canonical `internal/app/` owners.
- **Config extraction:** `internal/config` centralised env loading; deduplicated `envIntOr` via `config.IntOr`.
- **`internal/peligrosa/` move:** Relocated the top-level `peligrosa/` directory into `internal/peligrosa/` (auth, invites, requests, registration, roles, routes).
- **Handler extractions (autowire → backup → library → hooks → catalog → jellyfin → sse → downloads):** Each flat `cmd/*.go` file moved into its corresponding `internal/app/<pkg>/` package with a `Handler` struct replacing package-level globals.
- **Client extractions:** `internal/clients/arr`, `gluetun`, `qbt`, `bazarr`, `jellyfin`, `procula` — each replaced raw inline HTTP helpers in `cmd/`.

### Phase 2.2 — Repo layer

Extracted typed SQL stores from inline queries scattered across `cmd/` and `peligrosa/`:

- **`internal/repo/roles`** — typed store for the roles table.
- **`internal/repo/invites`** — typed store for the invites table; simplified `InsertRedemption` to plain exec.
- **`internal/repo/requests`** — typed store for the requests table.
- **`internal/repo/sessions`** — typed store for sessions + rate-limit tables; wired DB-backed rate-limit into auth (replaced in-memory map).
- **`internal/repo/catalog`** — typed store for the catalog table; propagated context through repo wrapper functions.
- **`internal/repo/dbutil`** — `ParseTime`, `Migrate` unified runner for both DBs.

### Phase 3.1–3.5 — Frontend (nginx static)

A five-pass overhaul of the dashboard's HTML/CSS/JS:

- **3.1 CSS custom properties:** Introduced `components.css` with shared UI primitives; deduped button and field rules from `setup.css` and `catalog.css`; wired strength classes to `register.js`.
- **3.2 JS modularisation:** Migrated `register.html` inline styles to `components.css`; tabbar hamburger nav at `<480px`.
- **3.3 Component extraction:** Drawers full-screen at `<768px`, modals full-screen at `<480px`; container queries for catalog, search, and library grids; fixed container query scope for `um-metrics`.
- **3.4 Responsive pass:** Stacked forms and full-width buttons at `<480px`.
- **3.5 Accessibility pass:** `wireSwitches()` for keyboard a11y on `role=switch` elements; documented import redirect shim.

---

## Current package inventory

### `middleware/internal/app/`

| Package | Purpose | Replaced |
|---|---|---|
| `app/` | `App` struct: owns all handlers, DB handles, and the HTTP server | `cmd/pelicula-api/main.go` (server lifecycle) |
| `bootstrap/` | One-shot startup sequence: DB init, auto-wire, queue start | `cmd/pelicula-api/main.go` (startup block) |
| `supervisor/` | Long-running goroutine supervisor with restart loop | `cmd/pelicula-api/main.go` (background goroutines) |
| `router/` | Route registration — wires all handlers onto the mux | `cmd/pelicula-api/main.go` (mux setup) |
| `actions/` | Action-bus registry and per-item action create handlers | `cmd/pelicula-api/actions.go` |
| `adminops/` | Admin rate-limiter, service restart, and log-streaming handlers | `cmd/pelicula-api/admin.go` |
| `autowire/` | *arr stack auto-wiring on startup | `cmd/pelicula-api/autowire.go` |
| `backup/` | Watchlist/library export and import-backup handlers | `cmd/pelicula-api/export.go` |
| `catalog/` | Catalog DB open, queue poller, catalog sync, catalog HTTP handlers | `cmd/pelicula-api/catalog.go`, `catalog_sync.go` |
| `downloads/` | Download list, pause/resume, cancel, category management handlers | `cmd/pelicula-api/downloads.go` (deleted) |
| `health/` | Health aggregation across all services: VPN, arr, jellyfin | `cmd/pelicula-api/health.go` |
| `hooks/` | Procula webhook handlers (import notify, job status) | `cmd/pelicula-api/hooks_*.go` |
| `jellyfin/` | Jellyfin env/auth wiring via `Wirer`; library handlers | `cmd/pelicula-api/jellyfin_*.go` |
| `library/` | Library scan, proxy, and apply handlers; `libraryRegistry` | `cmd/pelicula-api/library_*.go`, `libraries.go` |
| `missingwatcher/` | Missing content watcher with backoff cooldown | `cmd/pelicula-api/missing_watcher.go` |
| `network/` | Per-container bandwidth stats handler — reads Docker stats via the docker-proxy client | _new feature (no predecessor)_ |
| `search/` | Unified search, add-to-arr, Prowlarr provider handlers | `cmd/pelicula-api/search.go` |
| `services/` | Typed `Clients` aggregator passed to all handlers | `cmd/pelicula-api/services.go` |
| `settings/` | Settings read/update/reset handlers with env file I/O | `cmd/pelicula-api/settings.go` |
| `setup/` | Setup wizard handlers and env writer | `cmd/pelicula-api/setup.go` |
| `sse/` | SSE hub and poller — live event broadcaster | `cmd/pelicula-api/sse.go`, `sse_poller.go` (deleted) |
| `sysinfo/` | Host info, speedtest, and system log handlers | `cmd/pelicula-api/sysinfo.go` |
| `vpnwatchdog/` | VPN port-forward watchdog FSM | `cmd/pelicula-api/vpn_watchdog.go` |

### `middleware/internal/clients/`

| Package | Purpose | Replaced |
|---|---|---|
| `arr/` | Typed client for Sonarr, Radarr, and Prowlarr APIs | Inline `arrGet()`/`arrPost()` helpers in `cmd/` |
| `apprise/` | Apprise notification client | Inline HTTP calls in `cmd/` |
| `bazarr/` | Typed client for Bazarr subtitle API | Inline `bzGet()`/`bzPostForm()` in `cmd/` |
| `docker/` | Docker socket-proxy client (restart, logs) | Inline HTTP calls in `cmd/` |
| `gluetun/` | Typed client for gluetun control API | Inline `gluetunGet()` in `cmd/` |
| `jellyfin/` | Typed client for Jellyfin API (auth, libraries, items) | Inline helpers in `cmd/pelicula-api/jellyfin_core.go` (deleted) |
| `procula/` | Typed client for procula job/status/notification API | Inline HTTP calls in `cmd/` |
| `qbt/` | Typed client for qBittorrent v5 API | Inline `qbtGet()`/`qbtPost()` in `cmd/` |

### `middleware/internal/repo/`

| Package | Purpose | Replaced |
|---|---|---|
| `catalog/` | Typed SQL store for the catalog table | Inline queries in `cmd/pelicula-api/catalog_db.go` (deleted) |
| `dbutil/` | `ParseTime` RFC3339 helper; `Migrate` shared runner for both DBs | Scattered dual-parse pairs and per-package migration runners |
| `invites/` | Typed SQL store for the invites table | Inline queries in `peligrosa/invites.go` |
| `migratejson/` | One-shot JSON→SQLite migration for legacy data | `cmd/pelicula-api/migrate_json.go` |
| `peliculadb/` | Shared pelicula DB handle and schema migration entrypoint | `cmd/pelicula-api/main.go` (DB open block) |
| `requests/` | Typed SQL store for the requests table | Inline queries in `peligrosa/requests.go` |
| `roles/` | Typed SQL store for the roles table | Inline queries in `peligrosa/auth.go` |
| `sessions/` | Typed SQL store for sessions and rate-limit tables | Inline queries + in-memory rate-limit map in `peligrosa/auth.go` |

### `middleware/internal/peligrosa/`

| Package | Purpose | Replaced |
|---|---|---|
| `auth.go` | Session validation, login/logout, rate-limit enforcement | `peligrosa/auth.go` (top-level dir, now deleted) |
| `invites.go` | Invite creation, redemption, expiry | `peligrosa/invites.go` |
| `loopback.go` | Loopback/self-request helper for internal API calls | `peligrosa/loopback.go` |
| `operators_http.go` | Operator CRUD HTTP handlers (moved from `cmd/`) | `cmd/pelicula-api/operators.go` |
| `register.go` | Open and invite-gated registration handlers | `peligrosa/register.go` |
| `requests.go` | Request queue: create, list, fulfil, reject | `peligrosa/requests.go` |
| `roles.go` | Role assignment and lookup helpers | `peligrosa/roles.go` |
| `routes.go` | Route wiring for all peligrosa endpoints | `peligrosa/routes.go` |

---

## Phase 4 tracker

Phase 4 covers three sub-phases: **4.1** drain `cmd/pelicula-api/` of handler files, **4.2** finish main.go diet and route extraction, **4.3** add test coverage.

| Task | Description | Status |
|---|---|---|
| T1 | Extract `internal/app/sysinfo` (host, speedtest, logs handlers) + `internal/clients/docker` + `internal/clients/apprise` | done |
| T2 | Extract `internal/repo/migratejson` (JSON→SQLite one-shot migration) | done |
| T3 | Extract `internal/app/services` (typed `Clients` aggregator) | done |
| T4 | Extract `internal/app/adminops` (admin rate-limiter, restart, log-stream handlers) | done |
| T5 | Extract `internal/app/actions` (action-bus registry and create handlers) | done |
| T6 | Move operator CRUD handlers into `internal/peligrosa` as `Auth` methods | done |
| T7 | Extract `internal/app/missingwatcher` (missing content watcher with backoff) | done |
| T8 | Extract `internal/app/vpnwatchdog` (VPN port-forward watchdog FSM) | done |
| T9 | Extract `internal/app/setup` (setup wizard handlers and env writer) | done |
| T10 | Fold `jellyfin_wiring.go` into `internal/app/jellyfin` via `Wirer`; complete `internal/clients/jellyfin` | done |
| T11 | Extract `internal/app/settings` (settings read/update/reset with env file I/O) | done |
| T12 | Extract `internal/app/search` (unified search, add-to-arr, Prowlarr providers) | done |
| T13 | Extract `internal/app/router` (route registration) | done |
| T14 | Move `adminops` construction out of `router.Register` | done |
| T15 | Extract `App`/`bootstrap`/`supervisor`; shrink `main.go` to ≤100 LOC | done |
| T16 | Delete all remaining drift files from `cmd/pelicula-api/` | done |
| T17 | Add handler tests for `internal/app/health` (≥70% coverage) | done |
| T18 | Rewrite `docs/REFACTOR_STATUS.md` as Phase 2/3 retrospective + Phase 4 tracker | done |
| T19 | Add handler tests for `internal/app/downloads` (≥60% coverage) | done |

---

## Known deferred

**procula flat-to-layered split** is explicitly out of scope for this refactor. The `procula/` service has its own flat structure mirroring the pre-refactor middleware layout. Applying the same `internal/app/` + `internal/repo/` pattern to procula is a separate project tracked in `docs/ROADMAP.md`.
