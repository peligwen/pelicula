// db.go — SQLite database setup and schema migration framework.
// Opens procula.db, enables WAL mode, and runs all schema migrations in version order.
package main

import (
	"database/sql"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"
)

// OpenDB opens (or creates) the SQLite database at path, configures WAL mode,
// then runs any pending schema migrations.
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
var migrations = []migration{
	{version: 1, up: migrate1},
	{version: 2, up: migrate2},
	{version: 3, up: migrate3},
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

// migrate2 adds the subs_acquired column for tracking Bazarr-delivered subtitles.
func migrate2(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN subs_acquired TEXT`)
	return err
}

// migrate3 adds action-bus discriminator columns to the jobs table.
// action_type defaults to 'pipeline' so legacy rows continue to route
// through the stage machine.
func migrate3(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE jobs ADD COLUMN action_type TEXT NOT NULL DEFAULT 'pipeline'`,
		`ALTER TABLE jobs ADD COLUMN params TEXT`,
		`ALTER TABLE jobs ADD COLUMN result TEXT`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// migrate1 creates the initial schema (version 1).
func migrate1(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS jobs (
			id                 TEXT PRIMARY KEY,
			created_at         TEXT NOT NULL,
			updated_at         TEXT NOT NULL,
			state              TEXT NOT NULL,
			stage              TEXT NOT NULL,
			progress           REAL NOT NULL DEFAULT 0,
			source             TEXT NOT NULL,
			validation         TEXT,
			missing_subs       TEXT,
			error              TEXT NOT NULL DEFAULT '',
			retry_count        INTEGER NOT NULL DEFAULT 0,
			manual_profile     TEXT NOT NULL DEFAULT '',
			dualsub_outputs    TEXT,
			dualsub_error      TEXT NOT NULL DEFAULT '',
			transcode_profile  TEXT NOT NULL DEFAULT '',
			transcode_decision TEXT NOT NULL DEFAULT '',
			transcode_outputs  TEXT,
			transcode_error    TEXT NOT NULL DEFAULT '',
			transcode_eta      REAL NOT NULL DEFAULT 0
		)`,

		`CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(40, len(stmt))], err)
		}
	}
	return nil
}

