// Package repo provides typed data-access stores for pelicula-api.
//
// Each subdirectory owns the SQL for its domain:
//   - catalog:  media catalog records (catalog.db, separate from pelicula.db;
//     schema lives in internal/app/catalog)
//   - invites:  one-time invite tokens and redemption history
//   - requests: viewer request queue, state machine, and audit events
//   - roles:    per-user role overrides and viewer-role mapping
//   - sessions: active session records with in-memory + SQLite hybrid persistence
//
// Shared infrastructure:
//   - dbutil:      Open (with busy_timeout, WAL, foreign-keys pragmas), Migrate,
//     ParseTime, FormatTime (RFC3339Nano, RFC3339 fallback)
//   - peliculadb:  pelicula.db schema owner; two migrations: v1 (base tables),
//     v2 (migrated_json_files bookkeeping table)
//   - migratejson: one-shot legacy JSON → SQLite importer; ctx-aware, each file
//     wrapped in a transaction, idempotent via migrated_json_files
package repo
