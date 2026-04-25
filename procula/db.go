// db.go — SQLite database setup and schema migration framework.
// Opens procula.db, enables WAL mode, and runs all schema migrations in version order.
package procula

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

// schemaVersion is the current schema version. Bump this when adding new migrations.
const schemaVersion = 10

// DDL shared between migrateBaseline and the corresponding incremental migrations.
// Keeping them as named constants ensures the two paths stay in sync.
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

	// ddlNotifications stores the dashboard notification feed in SQLite.
	// Replaces the JSONL file (notifications_feed.json) used by older procula versions.
	ddlNotifications = `CREATE TABLE IF NOT EXISTS notifications (
		id         TEXT PRIMARY KEY,
		timestamp  TEXT NOT NULL,
		type       TEXT NOT NULL,
		title      TEXT NOT NULL,
		year       INTEGER NOT NULL DEFAULT 0,
		media_type TEXT NOT NULL DEFAULT '',
		message    TEXT NOT NULL,
		detail     TEXT NOT NULL DEFAULT '',
		job_id     TEXT NOT NULL DEFAULT ''
	)`

	ddlNotificationsIdx = `CREATE INDEX IF NOT EXISTS idx_notifications_timestamp ON notifications(timestamp)`

	// ddlJobsIndexState indexes the most common queue WHERE clause.
	ddlJobsIndexState = `CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state)`
	// ddlJobsIndexCreatedAt supports ORDER BY created_at used in List().
	ddlJobsIndexCreatedAt = `CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at)`
	// ddlJobsIndexActionType supports action_type filtering in List().
	ddlJobsIndexActionType = `CREATE INDEX IF NOT EXISTS idx_jobs_action_type ON jobs(action_type)`
)

// migrations is the ordered list of incremental schema migrations for existing installs.
// New installs bypass these via migrateBaseline (see runMigrations).
var migrations = []migration{
	{version: 1, up: migrate1},
	{version: 2, up: migrate2},
	{version: 3, up: migrate3},
	{version: 4, up: migrate4},
	{version: 5, up: migrate5},
	{version: 6, up: migrate6},
	{version: 7, up: migrate7},
	{version: 8, up: migrate8},
	{version: 9, up: migrate9},
	{version: 10, up: migrate10},
}

// runMigrations reads the current schema version and applies all pending migrations.
// Fresh installs (user_version == 0) use migrateBaseline to reach the current schema
// in a single transaction. Existing installs replay only the incremental steps they
// have not yet applied.
func runMigrations(db *sql.DB) error {
	ver, err := currentVersion(db)
	if err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	if ver == 0 {
		slog.Info("applying DB baseline schema", "component", "db", "version", schemaVersion)
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin baseline migration: %w", err)
		}
		if err := migrateBaseline(tx); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("baseline migration: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion)); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("set user_version %d: %w", schemaVersion, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit baseline migration: %w", err)
		}
		slog.Info("DB baseline schema applied", "component", "db", "version", schemaVersion)
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

// migrateBaseline creates the full v0.1 schema in a single pass for fresh installs.
// It consolidates all incremental migrations (migrate1–migrate7) into one DDL block.
// Existing installs with user_version > 0 use the incremental migration path instead.
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
			next_attempt_at    TEXT    DEFAULT NULL,
			interrupt_count    INTEGER NOT NULL DEFAULT 0
		)`,
		ddlSettings,
		ddlCatalogFlags,
		ddlCatalogFlagsIndex,
		ddlDualsubProfiles,
		ddlBlockedReleases,
		ddlNotifications,
		ddlNotificationsIdx,
		ddlJobsIndexState,
		ddlJobsIndexCreatedAt,
		ddlJobsIndexActionType,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(40, len(stmt))], err)
		}
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

// migrate4 adds the catalog column to persist CatalogInfo (jellyfin_synced, notification_sent).
func migrate4(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN catalog TEXT`)
	return err
}

// migrate5 adds the flags column to jobs and creates the catalog_flags
// index table (path → aggregated flag list + top severity) used by the
// catalog dashboard "Needs Attention" section.
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

// migrate6 creates the dualsub_profiles table for storing named dual-subtitle
// render profiles (JSON blobs keyed by profile name).
func migrate6(tx *sql.Tx) error {
	_, err := tx.Exec(ddlDualsubProfiles)
	return err
}

// migrate7 creates the blocked_releases table for tracking releases that have
// been blocked and removed from the *arr queue.
func migrate7(tx *sql.Tx) error {
	_, err := tx.Exec(ddlBlockedReleases)
	return err
}

// migrate8 adds the next_attempt_at column used by the exponential-backoff
// retry policy. NULL means the job is immediately eligible; a non-NULL value
// defers re-execution until that UTC timestamp has passed.
func migrate8(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN next_attempt_at TEXT DEFAULT NULL`)
	return err
}

// migrate9 adds:
//   - notifications table (replaces JSONL feed file)
//   - query-performance indexes on the jobs table (state, created_at, action_type)
func migrate9(tx *sql.Tx) error {
	for _, s := range []string{
		ddlNotifications,
		ddlNotificationsIdx,
		ddlJobsIndexState,
		ddlJobsIndexCreatedAt,
		ddlJobsIndexActionType,
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// migrate10 adds interrupt_count to separate process-kill interruptions from
// transient job-level failures, so restarts don't consume retry_count budget.
func migrate10(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN interrupt_count INTEGER NOT NULL DEFAULT 0`)
	return err
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
		ddlSettings,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(40, len(stmt))], err)
		}
	}
	return nil
}
