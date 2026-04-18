package dbutil_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"pelicula-api/internal/repo/dbutil"
)

// openMemDB opens a fresh in-memory SQLite database for testing.
func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// userVersion reads PRAGMA user_version from db.
func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	return v
}

// createTable is a test migration helper that creates a named table.
func createTable(name string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS ` + name + ` (id INTEGER PRIMARY KEY)`)
		return err
	}
}

func TestMigrate_FreshDB_AllMigrationsApplied(t *testing.T) {
	t.Parallel()
	db := openMemDB(t)

	migrations := []dbutil.Migration{
		{Version: 1, Up: createTable("t1")},
		{Version: 2, Up: createTable("t2")},
		{Version: 3, Up: createTable("t3")},
	}

	if err := dbutil.Migrate(db, migrations, "test"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if got := userVersion(t, db); got != 3 {
		t.Errorf("user_version = %d, want 3", got)
	}

	for _, table := range []string{"t1", "t2", "t3"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %q not found after migration", table)
		} else if err != nil {
			t.Errorf("query table %q: %v", table, err)
		}
	}
}

func TestMigrate_AlreadyMigrated_Idempotent(t *testing.T) {
	t.Parallel()
	db := openMemDB(t)

	migrations := []dbutil.Migration{
		{Version: 1, Up: createTable("t1")},
	}

	// First run.
	if err := dbutil.Migrate(db, migrations, "test"); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if got := userVersion(t, db); got != 1 {
		t.Errorf("user_version after first run = %d, want 1", got)
	}

	// Second run — must not fail or reset anything.
	if err := dbutil.Migrate(db, migrations, "test"); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if got := userVersion(t, db); got != 1 {
		t.Errorf("user_version after second run = %d, want 1", got)
	}
}

func TestMigrate_AddNewMigration_AppliesToExistingDB(t *testing.T) {
	t.Parallel()
	db := openMemDB(t)

	// Migrate to v1.
	v1 := []dbutil.Migration{
		{Version: 1, Up: createTable("t1")},
	}
	if err := dbutil.Migrate(db, v1, "test"); err != nil {
		t.Fatalf("initial Migrate: %v", err)
	}
	if got := userVersion(t, db); got != 1 {
		t.Errorf("user_version after v1 = %d, want 1", got)
	}

	// Now add v2 and re-run — only v2 should be applied.
	v2 := []dbutil.Migration{
		{Version: 1, Up: createTable("t1")},
		{Version: 2, Up: createTable("t2")},
	}
	if err := dbutil.Migrate(db, v2, "test"); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if got := userVersion(t, db); got != 2 {
		t.Errorf("user_version after v2 = %d, want 2", got)
	}

	// t1 must still exist.
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='t1'`).Scan(&name)
	if err == sql.ErrNoRows {
		t.Error("t1 missing after incremental migration")
	} else if err != nil {
		t.Errorf("query t1: %v", err)
	}
}

func TestMigrate_FailedMigration_RolledBack(t *testing.T) {
	t.Parallel()
	db := openMemDB(t)

	// Apply v1 successfully.
	good := []dbutil.Migration{
		{Version: 1, Up: createTable("t1")},
	}
	if err := dbutil.Migrate(db, good, "test"); err != nil {
		t.Fatalf("initial Migrate: %v", err)
	}

	// v2 has bad SQL — should be rolled back.
	bad := []dbutil.Migration{
		{Version: 1, Up: createTable("t1")},
		{
			Version: 2,
			Up: func(tx *sql.Tx) error {
				_, err := tx.Exec(`THIS IS NOT VALID SQL!!!`)
				return err
			},
		},
	}
	err := dbutil.Migrate(db, bad, "test")
	if err == nil {
		t.Fatal("expected error from bad migration, got nil")
	}

	// DB must remain at version 1.
	if got := userVersion(t, db); got != 1 {
		t.Errorf("user_version after failed migration = %d, want 1 (rollback)", got)
	}

	// t1 must still exist.
	var name string
	qerr := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='t1'`).Scan(&name)
	if qerr == sql.ErrNoRows {
		t.Error("t1 was lost after failed migration — unexpected")
	} else if qerr != nil {
		t.Errorf("query t1 after failure: %v", qerr)
	}
}

func TestMigrate_EmptyMigrations_NoOp(t *testing.T) {
	t.Parallel()
	db := openMemDB(t)

	if err := dbutil.Migrate(db, nil, "test"); err != nil {
		t.Fatalf("Migrate with no migrations: %v", err)
	}
	if got := userVersion(t, db); got != 0 {
		t.Errorf("user_version = %d, want 0 (untouched)", got)
	}
}
