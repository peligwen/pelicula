package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// testDB creates a fresh SQLite database in t.TempDir() and returns it.
// The database is closed automatically when the test ends.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("testDB: OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenDB_CreatesTablesAndSetsVersion(t *testing.T) {
	db := testDB(t)

	// Verify schema version was set.
	ver, err := currentVersion(db)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if ver != 1 {
		t.Errorf("user_version = %d, want 1", ver)
	}

	// Verify all expected tables exist.
	tables := []string{
		"roles", "invites", "redemptions",
		"requests", "request_events",
		"sessions", "dismissed_jobs", "rate_limits",
	}
	for _, table := range tables {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %q not found", table)
		} else if err != nil {
			t.Errorf("query table %q: %v", table, err)
		}
	}
}

func TestOpenDB_MigratesForwardFromZero(t *testing.T) {
	// Open raw SQLite without running migrations (user_version = 0).
	path := filepath.Join(t.TempDir(), "bare.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	raw.Close()

	// Now open via OpenDB — should migrate from 0 → 1.
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	ver, err := currentVersion(db)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if ver != 1 {
		t.Errorf("user_version = %d, want 1", ver)
	}
}

func TestOpenDB_IdempotentOnSecondOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent.db")

	db1, err := OpenDB(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	// Second open must not fail and must not reset the version.
	db2, err := OpenDB(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	ver, err := currentVersion(db2)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if ver != 1 {
		t.Errorf("user_version = %d after second open, want 1", ver)
	}
}
