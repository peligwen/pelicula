// Package db provides SQLite database setup and schema migration for procula.
package db

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

// SchemaVersion is the current schema version. Bump this when adding new migrations.
const SchemaVersion = 8

// DDL shared between migrateBaseline and the corresponding incremental migrations.
const (
	ddlSettings = `CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`

	ddlCatalogFlags = `CREATE TABLE IF NOT EXISTS catalog_flags (
		path       TEXT PRIMARY KEY,
		flags      TEXT NOT NULL,
		severity   TEXT NOT NULL,
		job_id     TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`

	ddlCatalogFlagsIndex = `CREATE INDEX IF NOT EXISTS idx_catalog_flags_severity ON catalog_flags(severity)`

	ddlDualsubProfiles = `CREATE TABLE IF NOT EXISTS dualsub_profiles (
		name TEXT PRIMARY KEY,
		data TEXT NOT NULL
	)`

	ddlBlockedReleases = `CREATE TABLE IF NOT EXISTS blocked_releases (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		arr_app          TEXT    NOT NULL,
		arr_blocklist_id INTEGER NOT NULL DEFAULT 0,
		arr_item_id      INTEGER NOT NULL,
		display_title    TEXT    NOT NULL,
		file_path        TEXT    NOT NULL,
		blocked_at       TEXT    NOT NULL,
		reason           TEXT    NOT NULL DEFAULT ''
	)`
)

// migrations is the ordered list of incremental schema migrations for existing installs.
var migrations = []migration{
	{version: 1, up: migrate1},
	{version: 2, up: migrate2},
	{version: 3, up: migrate3},
	{version: 4, up: migrate4},
	{version: 5, up: migrate5},
	{version: 6, up: migrate6},
	{version: 7, up: migrate7},
	{version: 8, up: migrate8},
}

// runMigrations reads the current schema version and applies all pending migrations.
func runMigrations(db *sql.DB) error {
	ver, err := currentVersion(db)
	if err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	if ver == 0 {
		slog.Info("applying DB baseline schema", "component", "db", "version", SchemaVersion)
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin baseline migration: %w", err)
		}
		if err := migrateBaseline(tx); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("baseline migration: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, SchemaVersion)); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("set user_version %d: %w", SchemaVersion, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit baseline migration: %w", err)
		}
		slog.Info("DB baseline schema applied", "component", "db", "version", SchemaVersion)
		return nil
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

// migrateBaseline creates the full schema in a single pass for fresh installs.
func migrateBaseline(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS jobs (
			id                 TEXT    PRIMARY KEY,
			created_at         TEXT    NOT NULL,
			updated_at         TEXT    NOT NULL,
			state              TEXT    NOT NULL,
			stage              TEXT    NOT NULL,
			progress           REAL    NOT NULL DEFAULT 0,
			source             TEXT    NOT NULL,
			validation         TEXT,
			missing_subs       TEXT,
			error              TEXT    NOT NULL DEFAULT '',
			retry_count        INTEGER NOT NULL DEFAULT 0,
			manual_profile     TEXT    NOT NULL DEFAULT '',
			dualsub_outputs    TEXT,
			dualsub_error      TEXT    NOT NULL DEFAULT '',
			transcode_profile  TEXT    NOT NULL DEFAULT '',
			transcode_decision TEXT    NOT NULL DEFAULT '',
			transcode_outputs  TEXT,
			transcode_error    TEXT    NOT NULL DEFAULT '',
			transcode_eta      REAL    NOT NULL DEFAULT 0,
			subs_acquired      TEXT,
			action_type        TEXT    NOT NULL DEFAULT 'pipeline',
			params             TEXT,
			result             TEXT,
			catalog            TEXT,
			flags              TEXT,
			next_attempt_at    TEXT    DEFAULT NULL
		)`,
		ddlSettings,
		ddlCatalogFlags,
		ddlCatalogFlagsIndex,
		ddlDualsubProfiles,
		ddlBlockedReleases,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			n := len(stmt)
			if n > 40 {
				n = 40
			}
			return fmt.Errorf("exec %q: %w", stmt[:n], err)
		}
	}
	return nil
}

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
		ddlSettings,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			n := len(stmt)
			if n > 40 {
				n = 40
			}
			return fmt.Errorf("exec %q: %w", stmt[:n], err)
		}
	}
	return nil
}

func migrate2(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN subs_acquired TEXT`)
	return err
}

func migrate3(tx *sql.Tx) error {
	for _, s := range []string{
		`ALTER TABLE jobs ADD COLUMN action_type TEXT NOT NULL DEFAULT 'pipeline'`,
		`ALTER TABLE jobs ADD COLUMN params TEXT`,
		`ALTER TABLE jobs ADD COLUMN result TEXT`,
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrate4(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN catalog TEXT`)
	return err
}

func migrate5(tx *sql.Tx) error {
	for _, s := range []string{
		`ALTER TABLE jobs ADD COLUMN flags TEXT`,
		ddlCatalogFlags,
		ddlCatalogFlagsIndex,
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrate6(tx *sql.Tx) error {
	_, err := tx.Exec(ddlDualsubProfiles)
	return err
}

func migrate7(tx *sql.Tx) error {
	_, err := tx.Exec(ddlBlockedReleases)
	return err
}

func migrate8(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN next_attempt_at TEXT DEFAULT NULL`)
	return err
}
