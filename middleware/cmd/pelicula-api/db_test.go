package main

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// currentVersion reads PRAGMA user_version from db (test helper).
func currentVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v, err
}

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
		"sessions", "rate_limits",
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

// TestSchemaEquivalence_PeliculaDB asserts that running all pelicula.db
// migrations produces the exact expected set of table names. This catches
// accidental migration renumbering (which would skip or double-apply steps).
func TestSchemaEquivalence_PeliculaDB(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	sort.Strings(tables)
	got := strings.Join(tables, ",")

	// Known-good snapshot after migration v1.
	// If this fails, a migration was renumbered, reordered, or the schema changed unexpectedly.
	const want = "invites,rate_limits,redemptions,request_events,requests,roles,sessions"
	if got != want {
		t.Errorf("schema mismatch\n  got:  %s\n  want: %s", got, want)
	}

	// Final user_version must equal the count of migrations.
	ver, err := currentVersion(db)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if ver != len(migrations) {
		t.Errorf("user_version = %d, want %d (len(migrations))", ver, len(migrations))
	}
}
