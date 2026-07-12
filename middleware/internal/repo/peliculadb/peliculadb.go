// Package peliculadb opens and migrates the primary pelicula SQLite database.
package peliculadb

import (
	"database/sql"
	"fmt"

	"pelicula-api/internal/repo/dbutil"
)

// Open opens (or creates) the SQLite database at path and runs all pending
// schema migrations.
func Open(path string) (*sql.DB, error) {
	return dbutil.Open(path, migrations, "peliculadb")
}

// migrations is the ordered list of all schema migrations for pelicula.db.
var migrations = []dbutil.Migration{
	{Version: 1, Up: migrate1},
	{Version: 2, Up: migrate2},
	{Version: 3, Up: migrate3},
	{Version: 4, Up: migrate4},
	{Version: 5, Up: migrate5},
}

func migrate1(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS roles (
			jellyfin_id TEXT PRIMARY KEY,
			username     TEXT NOT NULL,
			role         TEXT NOT NULL
		)`,
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
		`CREATE TABLE IF NOT EXISTS redemptions (
			invite_token TEXT NOT NULL REFERENCES invites(token) ON DELETE CASCADE,
			username     TEXT NOT NULL,
			jellyfin_id  TEXT NOT NULL,
			redeemed_at  TEXT NOT NULL
		)`,
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
		`CREATE TABLE IF NOT EXISTS request_events (
			request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
			at         TEXT NOT NULL,
			state      TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			note       TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_request_id ON request_events(request_id)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			username   TEXT NOT NULL,
			role       TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
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

func migrate2(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS migrated_json_files (
		filename    TEXT PRIMARY KEY,
		migrated_at TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create migrated_json_files: %w", err)
	}
	return nil
}

// migrate3 drops the ON DELETE CASCADE on redemptions.invite_token. redemptions
// is an append-only audit log (who redeemed which invite, and when); deleting
// an invite is a routine admin-hygiene action and should not erase that
// history. SQLite has no ALTER TABLE ... DROP CONSTRAINT, so the fix is the
// standard rebuild: recreate the table without the FK action, copy rows over,
// drop the old table, rename. Orphaned invite_token values afterward are
// harmless for a read-only history table.
func migrate3(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE redemptions_new (
			invite_token TEXT NOT NULL,
			username     TEXT NOT NULL,
			jellyfin_id  TEXT NOT NULL,
			redeemed_at  TEXT NOT NULL
		)`,
		`INSERT INTO redemptions_new (invite_token, username, jellyfin_id, redeemed_at)
			SELECT invite_token, username, jellyfin_id, redeemed_at FROM redemptions`,
		`DROP TABLE redemptions`,
		`ALTER TABLE redemptions_new RENAME TO redemptions`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(40, len(stmt))], err)
		}
	}
	return nil
}

// migrate4 adds requests.seasons: a viewer/admin's desired season-level
// scope for a series request. The empty string (the DEFAULT, and every
// pre-migration row) means unspecified/all — the historical behavior before
// season support existed; a non-empty value is a JSON int array as text,
// e.g. "[1,2]". See internal/repo/requests/store.go's
// seasonsToText/seasonsFromText helpers.
func migrate4(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE requests ADD COLUMN seasons TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("add requests.seasons column: %w", err)
	}
	return nil
}

// migrate5 adds requests.available_seen_at: tracks whether the requester has
// seen/acknowledged an "available" request in the dashboard. The empty
// string (the DEFAULT, and every pre-migration row) means unseen; a
// non-empty value is an RFC3339Nano timestamp recording when the requester
// acknowledged it. See internal/repo/requests/store.go's
// ListUnseenAvailableByUser/MarkAvailableSeen.
func migrate5(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE requests ADD COLUMN available_seen_at TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("add requests.available_seen_at column: %w", err)
	}
	return nil
}
