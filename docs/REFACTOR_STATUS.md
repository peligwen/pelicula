# Middleware Refactor Status

Phase 2 of the architectural refactor established the `internal/` skeleton and wired the highest-impact packages. This document tracks which packages are actively used by production code, which are scaffolding stubs awaiting Phase 2.1 extraction, and the criteria for each.

## Wired and live

These packages are imported by production code and carry real logic.

| Package | Imported by | Notes |
|---|---|---|
| `internal/httpx` | `internal/clients/*` | Shared HTTP base with retry/redaction |
| `internal/clients/arr` | `cmd/pelicula-api/services.go`, `catalog_sync.go` | Sonarr/Radarr/Prowlarr typed client |
| `internal/clients/gluetun` | `cmd/pelicula-api/health.go`, `vpn_watchdog.go` | Replaces raw `gluetunGet()` |
| `internal/clients/qbt` | `cmd/pelicula-api/services.go` | qBittorrent typed client |
| `internal/clients/bazarr` | `cmd/pelicula-api/autowire.go` | Replaces raw `bzGet()`/`bzPostForm()` |
| `internal/clients/procula` | `cmd/pelicula-api/main.go` | Procula job/status/notification client |
| `internal/repo/dbutil` | `internal/peligrosa/auth.go`, `internal/peligrosa/invites.go`, `internal/peligrosa/requests.go` | `ParseTime` replaces all RFC3339 dual-parse pairs |
| `internal/app/catalog` | `cmd/pelicula-api/main.go` | `OpenCatalogDB`, `RunQueuePoller` — used at startup |
| `internal/app/downloads` | `cmd/pelicula-api/main.go` | `downloads.Handler` wired for all download mux routes |
| `internal/app/health` | `cmd/pelicula-api/main.go` | `health.Handler` wired for `/api/pelicula/health` |
| `internal/app/sse` | `cmd/pelicula-api/main.go` | `sse.Hub` + `sse.Poller` are the live SSE broadcaster and handler |
| `internal/peligrosa` | `cmd/pelicula-api/main.go`, `globals.go`, `operators.go`, `export.go`, `migrate_json.go` | Moved from `peligrosa/`; auth, invites, requests, registration, roles, routes |

## Scaffolding stubs (Phase 2.1 targets)

These directories contain `doc.go` or minimal stubs. They exist to establish the target package layout. Each will be filled when the corresponding flat file in `cmd/pelicula-api/` is extracted during Phase 2.1 (package split).

| Package | Source file(s) to extract | Phase 2.1 step |
|---|---|---|
| `internal/app/autowire` | `cmd/pelicula-api/autowire.go` (~450 LOC) | Package split |
| `internal/app/backup` | `cmd/pelicula-api/export.go` (~750 LOC) | Package split |
| `internal/app/library` | `cmd/pelicula-api/library_scan.go`, `library_proxy.go`, `library_apply.go` | Package split |
| ~~`internal/peligrosa`~~ | ~~`peligrosa/` subpackage — move to `internal/peligrosa/`~~ | Done — see "Wired and live" |
| `internal/repo` | Full SQL repo per table (invites, requests, roles, sessions, catalog) | Phase 2.2 repo layer |
| `internal/config` | `cmd/pelicula-api/main.go` env loading | Package split |

## Known drift (Phase 2.1 cleanup targets)

These are parallel implementations that exist in both the cmd package and an internal package. They use the same DB handle so there is no runtime bug, but they will drift if not consolidated.

| Issue | Location | Plan |
|---|---|---|
| Parallel catalog DB | `cmd/catalog_db.go` and `internal/app/catalog/db.go` define identical `CatalogItem`, `OpenCatalogDB`, `UpsertCatalogItem`, etc. | Phase 2.1: delete `cmd/catalog_db.go`, have cmd handlers import from `internal/app/catalog` |
| Legacy SSE types | `cmd/sse.go` (`SSEHub`) and `cmd/sse_poller.go` (`SSEPoller`) are tested but no longer wired to the mux | Phase 2.1: delete after tests migrate to `internal/app/sse` |

## Deleted (not deferred)

These were created or existed as parallel implementations and have been removed:

| Package / File | Reason |
|---|---|
| `internal/app/hooks/hooks.go` | Complete parallel implementation of `hooks_*.go`; neither was the right authority — removed to eliminate ambiguity. The cmd-package `hooks_*.go` files remain the live handlers. Will be re-extracted to `internal/app/hooks/` as part of Phase 2.1. |
| `internal/services/clients.go` | 379-line parallel implementation of `cmd/services.go`; zero live importers. Deleted. |
| `cmd/downloads.go` | Dead: all four handlers replaced by `internal/app/downloads.Handler`. |
| `cmd/arrqueue.go` | Dead: only called from the now-deleted `cmd/downloads.go`. Queue logic lives in `internal/app/downloads`. |
| `internal/clients/jellyfin` | `jellyfin_core.go` is 673 LOC; full migration is a Phase 2.1 item. |
| `internal/repo/invites`, `requests`, `roles`, `sessions` | Full SQL migration deferred to Phase 2.2; `dbutil.ParseTime` is the only Phase 2 extraction. |

## Criteria for moving a stub to wired

1. At least one production call site in `cmd/pelicula-api/` imports and uses it.
2. The package has a test or is covered by an integration test.
3. The corresponding flat file in `cmd/pelicula-api/` is either removed or marked for removal.

## Criteria for deleting a stub

A stub is deleted (not deferred) when it adds confusion without value (e.g., a full parallel implementation that is never wired). It is re-created when the Phase 2.1 extraction begins.
