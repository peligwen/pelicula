// Package store is the single SQLite database behind the server. It holds
// only what no other service in the stack already owns: who may do what
// (roles, sessions, invites), what viewers asked for (requests), what the
// pipeline did with each import (jobs), and a handful of runtime settings.
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

// Role is a Pelicula permission level. Jellyfin owns identity; this is the
// only authorization concept Pelicula adds on top.
type Role string

const (
	RoleViewer  Role = "viewer"  // search, request, see own requests and jobs
	RoleManager Role = "manager" // plus add directly, approve requests, manage downloads
	RoleAdmin   Role = "admin"   // plus settings, invites, roles
)

// ParseRole validates a role string.
func ParseRole(s string) (Role, bool) {
	switch Role(s) {
	case RoleViewer, RoleManager, RoleAdmin:
		return Role(s), true
	}
	return "", false
}

func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleManager:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// AtLeast reports whether r grants at least min.
func (r Role) AtLeast(min Role) bool { return r.rank() >= min.rank() }

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

const schemaVersion = 1

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
	if v < 1 {
		if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
			return fmt.Errorf("store: migrate v1: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS roles (
  username   TEXT PRIMARY KEY,
  role       TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token            TEXT PRIMARY KEY,
  username         TEXT NOT NULL,
  role             TEXT NOT NULL,
  jellyfin_user_id TEXT NOT NULL DEFAULT '',
  jellyfin_token   TEXT NOT NULL DEFAULT '',
  expires_at       TEXT NOT NULL,
  created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_username ON sessions(username);
CREATE TABLE IF NOT EXISTS invites (
  code       TEXT PRIMARY KEY,
  role       TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_by    TEXT NOT NULL DEFAULT '',
  used_at    TEXT
);
CREATE TABLE IF NOT EXISTS requests (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  media_type   TEXT NOT NULL,
  tmdb_id      INTEGER NOT NULL DEFAULT 0,
  tvdb_id      INTEGER NOT NULL DEFAULT 0,
  title        TEXT NOT NULL,
  year         INTEGER NOT NULL DEFAULT 0,
  poster       TEXT NOT NULL DEFAULT '',
  requested_by TEXT NOT NULL,
  status       TEXT NOT NULL,
  arr_id       INTEGER NOT NULL DEFAULT 0,
  decided_by   TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS requests_user ON requests(requested_by);
CREATE INDEX IF NOT EXISTS requests_status ON requests(status);
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
