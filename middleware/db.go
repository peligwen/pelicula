// db.go — SQLite database setup and schema migration framework.
// Opens the shared pelicula.db, enables WAL mode + foreign keys, and runs
// all schema migrations in version order.
package main

import (
	"database/sql"
	"fmt"
	"log/slog"

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

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return db, nil
}

// currentVersion reads the PRAGMA user_version from the database.
func currentVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// migration is a single schema migration step.
type migration struct {
	version int
	up      func(tx *sql.Tx) error
}

// migrations is the ordered list of all schema migrations.
// Each migration runs in a transaction and sets PRAGMA user_version on success.
var migrations = []migration{
	{version: 1, up: migrate1},
}

// runMigrations reads the current schema version and applies all pending
// migrations in order.
func runMigrations(db *sql.DB) error {
	ver, err := currentVersion(db)
	if err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= ver {
			continue
		}
		slog.Info("applying DB migration", "component", "db", "version", m.version)
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		// SQLite does not allow PRAGMA user_version inside a transaction via
		// the parameter syntax, so we use string formatting (the value is
		// an int literal from our own code — not user input).
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, m.version)); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("set user_version %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
		slog.Info("DB migration applied", "component", "db", "version", m.version)
	}
	return nil
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
