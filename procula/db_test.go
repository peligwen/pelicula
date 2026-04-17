package procula

import (
	"database/sql"
	"errors"
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
	if ver != 9 {
		t.Errorf("user_version = %d, want 9", ver)
	}

	tables := []string{"jobs", "settings"}
	for _, table := range tables {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			t.Errorf("table %q not found", table)
		} else if err != nil {
			t.Errorf("query table %q: %v", table, err)
		}
	}
}

func TestMigrate3AddsActionColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if ver < 3 {
		t.Fatalf("user_version = %d, want >= 3", ver)
	}

	rows, err := db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	for _, want := range []string{"action_type", "params", "result"} {
		if !cols[want] {
			t.Errorf("missing column %q", want)
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
	if ver != 9 {
		t.Errorf("user_version = %d after second open, want 9", ver)
	}
}

func TestMigrate5AddsFlagSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if ver < 5 {
		t.Fatalf("user_version = %d, want >= 5", ver)
	}

	// jobs.flags column exists
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	rows.Close()
	if !cols["flags"] {
		t.Errorf("jobs.flags column missing")
	}

	// catalog_flags table exists with expected columns
	cols = map[string]bool{}
	rows, err = db.Query(`PRAGMA table_info(catalog_flags)`)
	if err != nil {
		t.Fatalf("table_info catalog_flags: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	rows.Close()
	for _, want := range []string{"path", "flags", "severity", "job_id", "updated_at"} {
		if !cols[want] {
			t.Errorf("catalog_flags.%s missing", want)
		}
	}
}

func TestMigrate6CreatesDualSubProfiles(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if ver < 6 {
		t.Fatalf("user_version = %d, want >= 6", ver)
	}

	// dualsub_profiles table exists with expected columns
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(dualsub_profiles)`)
	if err != nil {
		t.Fatalf("table_info dualsub_profiles: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	rows.Close()
	for _, want := range []string{"name", "data"} {
		if !cols[want] {
			t.Errorf("dualsub_profiles.%s missing", want)
		}
	}
}

func TestMigrate7CreatesBlockedReleases(t *testing.T) {
	db := testDB(t)

	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if ver < 7 {
		t.Fatalf("user_version = %d, want >= 7", ver)
	}

	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(blocked_releases)`)
	if err != nil {
		t.Fatalf("table_info blocked_releases: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	rows.Close()
	for _, want := range []string{"id", "arr_app", "arr_blocklist_id", "arr_item_id", "display_title", "file_path", "blocked_at", "reason"} {
		if !cols[want] {
			t.Errorf("blocked_releases.%s missing", want)
		}
	}
}
