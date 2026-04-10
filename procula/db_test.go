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

	ver, err := currentVersion(db)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if ver != 2 {
		t.Errorf("user_version = %d, want 2", ver)
	}

	tables := []string{"jobs", "settings"}
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

func TestOpenDB_IdempotentOnSecondOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent.db")

	db1, err := OpenDB(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	db2, err := OpenDB(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	ver, err := currentVersion(db2)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if ver != 2 {
		t.Errorf("user_version = %d after second open, want 2", ver)
	}
}
