// db.go — SQLite database setup and schema migration framework.
// Opens the shared pelicula.db, enables WAL mode + foreign keys, and runs
// all schema migrations in version order.
package main

import (
	"database/sql"
	"fmt"

	"pelicula-api/internal/repo/dbutil"

	_ "modernc.org/sqlite"
)

// OpenDB opens (or creates) the SQLite database at path, configures WAL mode
// and foreign key enforcement, then runs any pending schema migrations.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	// SQLite is not safe for concurrent writes from multiple connections without
	// WAL mode. Use a single connection to avoid SQLITE_BUSY under load.
	db.SetMaxOpenConns(1)

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	// Enforce foreign key constraints (SQLite disables them by default).
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := dbutil.Migrate(db, migrations, "db"); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return db, nil
}

// migrations is the ordered list of all schema migrations for pelicula.db.
// Each migration runs in a transaction via dbutil.Migrate.
var migrations = []dbutil.Migration{
	{Version: 1, Up: migrate1},
}

// migrate1 creates the initial schema (version 1).
func migrate1(tx *sql.Tx) error {
	stmts := []string{
		// Roles: Jellyfin user ID → Pelicula role mapping.
		`CREATE TABLE IF NOT EXISTS roles (
			jellyfin_id TEXT PRIMARY KEY,
			username     TEXT NOT NULL,
			role         TEXT NOT NULL
		)`,

		// Invites: invite token lifecycle.
		`CREATE TABLE IF NOT EXISTS invites (
			token      TEXT PRIMARY KEY,
			label      TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			created_by TEXT NOT NULL,
			expires_at TEXT,
			max_uses   INTEGER,
			uses       INTEGER NOT NULL DEFAULT 0,
			revoked    INTEGER NOT NULL DEFAULT 0
		)`,

		// Redemptions: audit records for invite use.
		`CREATE TABLE IF NOT EXISTS redemptions (
			invite_token TEXT NOT NULL REFERENCES invites(token) ON DELETE CASCADE,
			username     TEXT NOT NULL,
			jellyfin_id  TEXT NOT NULL,
			redeemed_at  TEXT NOT NULL
		)`,

		// Requests: viewer media request queue.
		`CREATE TABLE IF NOT EXISTS requests (
			id           TEXT PRIMARY KEY,
			type         TEXT NOT NULL,
			tmdb_id      INTEGER NOT NULL DEFAULT 0,
			tvdb_id      INTEGER NOT NULL DEFAULT 0,
			title        TEXT NOT NULL,
			year         INTEGER NOT NULL DEFAULT 0,
			poster       TEXT NOT NULL DEFAULT '',
			requested_by TEXT NOT NULL DEFAULT '',
			state        TEXT NOT NULL,
			reason       TEXT NOT NULL DEFAULT '',
			arr_id       INTEGER NOT NULL DEFAULT 0,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)`,

		// Request events: state transition audit log.
		`CREATE TABLE IF NOT EXISTS request_events (
			request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
			at         TEXT NOT NULL,
			state      TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			note       TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_request_id ON request_events(request_id)`,

		// Sessions: authenticated user sessions.
		`CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			username   TEXT NOT NULL,
			role       TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,

		// Rate limits: failed login attempt tracking per IP.
		`CREATE TABLE IF NOT EXISTS rate_limits (
			ip           TEXT PRIMARY KEY,
			fail_count   INTEGER NOT NULL DEFAULT 0,
			window_start TEXT NOT NULL
		)`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(40, len(stmt))], err)
		}
	}
	return nil
}
