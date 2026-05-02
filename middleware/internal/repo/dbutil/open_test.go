package dbutil_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"pelicula-api/internal/repo/dbutil"
)

func TestOpen_WALEnabled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wal.db")
	db, err := dbutil.Open(path, nil, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestOpen_ForeignKeysEnabled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fk.db")
	db, err := dbutil.Open(path, nil, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpen_BusyTimeoutSet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := dbutil.Open(path, nil, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var timeout int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}
}

func TestOpen_MaxOpenConnsIs1(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conns.db")
	db, err := dbutil.Open(path, nil, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestOpen_RunsMigrations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "migrate.db")

	migrations := []dbutil.Migration{
		{Version: 1, Up: createTable("m1")},
		{Version: 2, Up: createTable("m2")},
	}
	db, err := dbutil.Open(path, migrations, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if got := userVersion(t, db); got != 2 {
		t.Errorf("user_version = %d, want 2", got)
	}
}

func TestOpen_PropagatesMigrationError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fail.db")

	errBoom := errors.New("boom")
	broken := []dbutil.Migration{
		{Version: 1, Up: func(_ *sql.Tx) error { return errBoom }},
	}

	db, err := dbutil.Open(path, broken, "test")
	if err == nil {
		db.Close()
		t.Fatal("expected error from failing migration, got nil")
	}
	if db != nil {
		t.Error("expected nil DB on error, got non-nil")
	}

	// A second Open with a healthy migration list must succeed, proving the
	// failed Open closed the DB cleanly (no fd leak).
	healthy := []dbutil.Migration{
		{Version: 1, Up: createTable("t1")},
	}
	db2, err := dbutil.Open(path, healthy, "test")
	if err != nil {
		t.Fatalf("second Open after failed first: %v", err)
	}
	db2.Close()
}
