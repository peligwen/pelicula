package peliculadb

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func currentVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	return v
}

func TestOpen_CreatesTablesAndSetsVersion(t *testing.T) {
	db := testDB(t)

	if got := currentVersion(t, db); got != 5 {
		t.Errorf("user_version = %d, want 5", got)
	}

	for _, table := range []string{
		"roles", "invites", "redemptions",
		"requests", "request_events",
		"sessions", "rate_limits", "migrated_json_files",
	} {
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

func TestOpen_MigratesForwardFromZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bare.db")

	// Open raw SQLite without running migrations — user_version stays 0.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	raw.Close()

	// Open via peliculadb.Open — must migrate 0 → 5.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if got := currentVersion(t, db); got != 5 {
		t.Errorf("user_version = %d, want 5", got)
	}
}

func TestOpen_IdempotentOnSecondOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	if got := currentVersion(t, db2); got != 5 {
		t.Errorf("user_version = %d after second open, want 5", got)
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

	// Known-good snapshot after all migrations.
	// If this fails, a migration was renumbered, reordered, or the schema changed unexpectedly.
	const want = "invites,migrated_json_files,rate_limits,redemptions,request_events,requests,roles,sessions"
	if got != want {
		t.Errorf("schema mismatch\n  got:  %s\n  want: %s", got, want)
	}

	// Final user_version must equal the count of migrations.
	if ver := currentVersion(t, db); ver != len(migrations) {
		t.Errorf("user_version = %d, want %d (len(migrations))", ver, len(migrations))
	}
}

// TestMigrate3_DeletingInviteDoesNotCascadeToRedemptions verifies MWD-5: the
// ON DELETE CASCADE on redemptions.invite_token was dropped in migrate3, so
// deleting an invite (a normal admin-hygiene action) leaves its redemption
// audit rows in place instead of silently erasing them.
func TestMigrate3_DeletingInviteDoesNotCascadeToRedemptions(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	if _, err := db.Exec(
		`INSERT INTO invites (token, label, created_at, created_by, max_uses, uses, revoked)
		 VALUES ('tok-1', '', '2026-01-01T00:00:00Z', 'admin', 1, 1, 0)`,
	); err != nil {
		t.Fatalf("insert invite: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO redemptions (invite_token, username, jellyfin_id, redeemed_at)
		 VALUES ('tok-1', 'alice', 'jf-1', '2026-01-01T00:01:00Z')`,
	); err != nil {
		t.Fatalf("insert redemption: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM invites WHERE token = 'tok-1'`); err != nil {
		t.Fatalf("delete invite: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM redemptions WHERE invite_token = 'tok-1'`).Scan(&count); err != nil {
		t.Fatalf("count redemptions: %v", err)
	}
	if count != 1 {
		t.Errorf("redemption rows after invite delete = %d, want 1 (audit trail must survive invite deletion)", count)
	}
}

// TestMigrate4_AddsSeasonsColumnWithEmptyDefault verifies migrate4: the
// requests table gains a seasons column that defaults to the empty string
// (unspecified/all) for both existing and newly-inserted rows.
func TestMigrate4_AddsSeasonsColumnWithEmptyDefault(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	if _, err := db.Exec(
		`INSERT INTO requests (id, type, title, state, created_at, updated_at)
		 VALUES ('req-1', 'movie', 'Test Movie', 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert request: %v", err)
	}

	var seasons string
	if err := db.QueryRow(`SELECT seasons FROM requests WHERE id = 'req-1'`).Scan(&seasons); err != nil {
		t.Fatalf("select seasons: %v", err)
	}
	if seasons != "" {
		t.Errorf("seasons = %q, want '' (default)", seasons)
	}
}

// TestMigrate5_AddsAvailableSeenAtColumnWithEmptyDefault verifies migrate5:
// the requests table gains an available_seen_at column that defaults to the
// empty string (unseen) for both existing and newly-inserted rows.
func TestMigrate5_AddsAvailableSeenAtColumnWithEmptyDefault(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	if _, err := db.Exec(
		`INSERT INTO requests (id, type, title, state, created_at, updated_at)
		 VALUES ('req-1', 'movie', 'Test Movie', 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert request: %v", err)
	}

	var seenAt string
	if err := db.QueryRow(`SELECT available_seen_at FROM requests WHERE id = 'req-1'`).Scan(&seenAt); err != nil {
		t.Fatalf("select available_seen_at: %v", err)
	}
	if seenAt != "" {
		t.Errorf("available_seen_at = %q, want '' (default/unseen)", seenAt)
	}
}
