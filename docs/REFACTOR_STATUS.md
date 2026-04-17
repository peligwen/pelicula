# Middleware Refactor Status

Phase 1 of the architectural refactor has established the `internal/` skeleton and wired the most impactful packages. This document tracks which packages are actively used, which are scaffolding stubs awaiting future phases, and the criteria for each.

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
| `internal/repo/dbutil` | `peligrosa/auth.go`, `peligrosa/invites.go` | `ParseTime` replaces duplicate RFC3339 parse pairs |
| `internal/app/catalog` | (inline — catalog_db.go re-exports) | Catalog DB and sync logic stubs |
| `internal/app/health` | (stub) | See below |
| `internal/app/sse` | (stub) | See below |
| `internal/app/downloads` | (stub) | See below |
| `internal/app/hooks` | (stub) | See below |
| `internal/services` | (stub) | See below |

## Scaffolding stubs (Phase 2 targets)

These directories contain doc.go or minimal stubs. They exist to establish the target package layout defined in the roadmap and are not yet imported by live code. They will be filled as Phase 2 proceeds — each receives real logic when the corresponding flat file in `cmd/pelicula-api/` is extracted.

| Package | Source file(s) to extract | Phase 2 step |
|---|---|---|
| `internal/app/autowire` | `cmd/pelicula-api/autowire.go` (450 LOC) | 2.1 package split |
| `internal/app/backup` | `cmd/pelicula-api/export.go` (752 LOC) | 2.1 package split |
| `internal/app/catalog` | `cmd/pelicula-api/catalog.go`, `catalog_sync.go`, `catalog_db.go` | 2.1 package split |
| `internal/app/downloads` | `cmd/pelicula-api/downloads.go` | 2.1 package split |
| `internal/app/health` | `cmd/pelicula-api/health.go`, `vpn_watchdog.go` | 2.1 package split |
| `internal/app/hooks` | `cmd/pelicula-api/hooks_*.go` | 2.1 package split |
| `internal/app/library` | `cmd/pelicula-api/library_scan.go`, `library_proxy.go`, `library_apply.go` | 2.1 package split |
| `internal/app/sse` | `cmd/pelicula-api/sse_hub.go`, `sse_poller.go` | 2.1 package split |
| `internal/peligrosa` | `peligrosa/` subpackage (keep, but split files) | 2.1 package split |
| `internal/repo` | Full SQL repo per table (invites, requests, roles, sessions, catalog) | 2.2 repo layer |
| `internal/services` | `cmd/pelicula-api/services.go` `ServiceClients` struct | 2.1 package split |
| `internal/config` | `cmd/pelicula-api/main.go` env loading | 2.1 package split |

## Deleted (not deferred)

These were created during Phase 1 exploration but removed after evaluation:

| Package | Reason |
|---|---|
| `internal/clients/jellyfin` | `jellyfin_core.go` is 673 LOC; full migration is a Phase 2 item, not a Phase 1 quick win |
| `internal/repo/invites`, `requests`, `roles`, `sessions` | Full SQL migration deferred to Phase 2.2; `dbutil.ParseTime` is the only Phase 1 extraction |

## Criteria for moving a stub to wired

A package graduates from stub to wired when:

1. At least one production call site in `cmd/pelicula-api/` imports and uses it
2. The package has a test or is covered by an integration test
3. The corresponding flat file in `cmd/pelicula-api/` is either removed or marked for removal in the next commit

## Criteria for deleting a stub

A stub is deleted (not deferred) when:

1. The planned extraction is out of scope for the current phase, **and**
2. The stub adds confusion without adding value (e.g., empty directory with only doc.go)

In that case the stub is deleted and re-created when Phase 2 begins that extraction.
