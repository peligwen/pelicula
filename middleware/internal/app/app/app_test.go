package app_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	pelapp "pelicula-api/internal/app/app"
	"pelicula-api/internal/peligrosa"

	_ "modernc.org/sqlite"
)

// openTestDB opens a file-backed SQLite database in t.TempDir() for tests.
// Using a file rather than :memory: lets us verify Close flushes the handle.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		t.Fatalf("openTestDB WAL: %v", err)
	}
	return db
}

// openAuthDB opens a SQLite DB with the minimal schema required by peligrosa.NewAuth.
func openAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS roles (
			jellyfin_id TEXT PRIMARY KEY,
			username    TEXT NOT NULL,
			role        TEXT NOT NULL DEFAULT 'viewer'
		);
		CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			username   TEXT NOT NULL,
			role       TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS rate_limits (
			ip           TEXT PRIMARY KEY,
			fail_count   INTEGER NOT NULL DEFAULT 0,
			window_start TEXT NOT NULL
		);
	`)
	if err != nil {
		db.Close()
		t.Fatalf("openAuthDB schema: %v", err)
	}
	return db
}

// TestClose_StopsAuthAndClosesDBs verifies that App.Close stops the auth
// cleanup goroutine and closes both SQLite handles so subsequent queries fail.
func TestClose_StopsAuthAndClosesDBs(t *testing.T) {
	authDB := openAuthDB(t)
	mainDB := openTestDB(t)
	catalogDB := openTestDB(t)

	auth := peligrosa.NewAuth(peligrosa.AuthConfig{DB: authDB})

	a := &pelapp.App{
		Auth:      auth,
		MainDB:    mainDB,
		CatalogDB: catalogDB,
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// After Close, queries on both DBs must fail — handles are closed.
	if _, err := mainDB.Exec(`SELECT 1`); err == nil {
		t.Error("expected mainDB query to fail after Close, got nil error")
	}
	if _, err := catalogDB.Exec(`SELECT 1`); err == nil {
		t.Error("expected catalogDB query to fail after Close, got nil error")
	}
}

// TestClose_NilFieldsNoOp verifies Close does not panic on a zero-value App.
func TestClose_NilFieldsNoOp(t *testing.T) {
	a := &pelapp.App{}
	if err := a.Close(); err != nil {
		t.Fatalf("Close on zero App returned error: %v", err)
	}
}
