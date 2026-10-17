// Package store is the single SQLite database behind the server. It holds
// only what no other service in the stack already owns: the dashboard's
// cookie sessions, what the pipeline did with each import (jobs), and a
// handful of runtime settings.
//
// Radarr, Sonarr, qBittorrent and Jellyfin remain the source of truth for the
// catalog, downloads and users; the API reads through to them.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the database. All methods are safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite file at path and runs
// migrations. The parent directory is created if missing.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// One writer at a time keeps SQLite happy under WAL and avoids
	// SQLITE_BUSY storms from the pipeline worker and HTTP handlers.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// OpenMemory opens an in-memory database. Intended for tests.
func OpenMemory() (*Store, error) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for tests and one-off maintenance.
func (s *Store) DB() *sql.DB { return s.db }

// ErrNotFound is returned by lookups that expect a row.
var ErrNotFound = errors.New("not found")

const schemaVersion = 2

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store: user_version: %w", err)
	}
	if v >= schemaVersion {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// schema brings any database below schemaVersion up to date in one step.
// Version 1 (the unreleased multi-user rescope) had roles, invites and
// requests tables, a role column on sessions and an auto-approve setting.
// Version 2 drops all of that: an old file loses its sessions, so everyone
// signs in again, and keeps its job history and settings.
const schema = `
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS requests;
DROP TABLE IF EXISTS sessions;
CREATE TABLE sessions (
  token      TEXT PRIMARY KEY,
  username   TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS jobs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  arr_type    TEXT NOT NULL,
  arr_id      INTEGER NOT NULL DEFAULT 0,
  episode_id  INTEGER NOT NULL DEFAULT 0,
  title       TEXT NOT NULL,
  path        TEXT NOT NULL,
  size        INTEGER NOT NULL DEFAULT 0,
  download_id TEXT NOT NULL DEFAULT '',
  runtime_min INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL,
  result      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  attempts    INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  started_at  TEXT,
  finished_at TEXT
);
CREATE INDEX IF NOT EXISTS jobs_status ON jobs(status);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
DELETE FROM settings WHERE key = 'auto_approve_requests';
`

// ── time helpers ─────────────────────────────────────────────────────────────

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nullTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

// now is overridable in tests.
var now = time.Now
