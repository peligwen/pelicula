package migratejson

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pelicula-api/internal/peligrosa"
	reporeqs "pelicula-api/internal/repo/requests"

	_ "modernc.org/sqlite"
)

// migrateTestFulfiller is a no-op Fulfiller for migrate tests.
type migrateTestFulfiller struct{}

func (f *migrateTestFulfiller) AddMovie(tmdbID, profileID int, rootPath string) (int, error) {
	return 0, nil
}
func (f *migrateTestFulfiller) AddSeries(tvdbID, profileID int, rootPath string) (int, error) {
	return 0, nil
}

// testDB creates a fresh SQLite database with the pelicula schema in t.TempDir()
// and returns it. The database is closed automatically when the test ends.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("testDB: sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		t.Fatalf("testDB: WAL: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		t.Fatalf("testDB: foreign_keys: %v", err)
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS roles (
			jellyfin_id TEXT PRIMARY KEY,
			username     TEXT NOT NULL,
			role         TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS invites (
			token      TEXT PRIMARY KEY,
			label      TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			created_by TEXT NOT NULL,
			expires_at TEXT,
			max_uses   INTEGER,
			uses       INTEGER NOT NULL DEFAULT 0,
			revoked    INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS redemptions (
			invite_token TEXT NOT NULL REFERENCES invites(token) ON DELETE CASCADE,
			username     TEXT NOT NULL,
			jellyfin_id  TEXT NOT NULL,
			redeemed_at  TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS requests (
			id           TEXT PRIMARY KEY,
			type         TEXT NOT NULL,
			tmdb_id      INTEGER NOT NULL DEFAULT 0,
			tvdb_id      INTEGER NOT NULL DEFAULT 0,
			title        TEXT NOT NULL,
			year         INTEGER NOT NULL DEFAULT 0,
			poster       TEXT NOT NULL DEFAULT '',
			requested_by TEXT NOT NULL DEFAULT '',
			state        TEXT NOT NULL,
			reason       TEXT NOT NULL DEFAULT '',
			arr_id       INTEGER NOT NULL DEFAULT 0,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS request_events (
			request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
			at         TEXT NOT NULL,
			state      TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			note       TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_request_id ON request_events(request_id)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			username   TEXT NOT NULL,
			role       TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS rate_limits (
			ip           TEXT PRIMARY KEY,
			fail_count   INTEGER NOT NULL DEFAULT 0,
			window_start TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("testDB: schema: %v", err)
		}
	}

	t.Cleanup(func() { db.Close() })
	return db
}

// ── migrateRolesJSON ──────────────────────────────────────────────────────────

func TestMigrateRolesJSON_NoFile(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	// Should be a no-op — no error, no rows.
	migrateRolesJSON(db, filepath.Join(dir, "roles.json"))
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 roles, got %d", count)
	}
}

func TestMigrateRolesJSON_Inserts(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.json")

	f := peligrosa.RolesFile{
		Version: 1,
		Users: []peligrosa.RolesEntry{
			{JellyfinID: "jf-001", Username: "alice", Role: peligrosa.RoleAdmin},
			{JellyfinID: "jf-002", Username: "bob", Role: peligrosa.RoleViewer},
		},
	}
	data, _ := json.Marshal(f)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	migrateRolesJSON(db, path)

	// File should be renamed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected roles.json to be renamed to .migrated")
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Errorf("expected .migrated file: %v", err)
	}

	// Verify rows in DB.
	store := peligrosa.NewRolesStore(db)
	role, ok := store.Lookup("jf-001")
	if !ok || role != peligrosa.RoleAdmin {
		t.Errorf("Lookup jf-001: role=%q ok=%v, want admin/true", role, ok)
	}
	role, ok = store.Lookup("jf-002")
	if !ok || role != peligrosa.RoleViewer {
		t.Errorf("Lookup jf-002: role=%q ok=%v, want viewer/true", role, ok)
	}
}

func TestMigrateRolesJSON_CorruptFile(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.json")
	os.WriteFile(path, []byte(`not valid json`), 0600)

	// Should not panic; corrupt file should be renamed to .corrupt (not .migrated).
	migrateRolesJSON(db, path)
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("expected .corrupt file after corrupt JSON: %v", err)
	}
	// Original file must be gone.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected original file to be renamed away")
	}
}

// ── migrateInvitesJSON ────────────────────────────────────────────────────────

func TestMigrateInvitesJSON_NoFile(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	migrateInvitesJSON(db, filepath.Join(dir, "invites.json"))
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM invites`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 invites, got %d", count)
	}
}

func TestMigrateInvitesJSON_Inserts(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "invites.json")

	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	maxUses := 3
	// 43-char URL-safe base64 token (same format as generateInviteToken).
	tok := "aaaabbbbccccddddeeeeffffgggghhhhiiijjjkklll"[:43]
	invites := []peligrosa.Invite{
		{
			Token:     tok,
			Label:     "Test label",
			CreatedAt: time.Now().UTC(),
			CreatedBy: "admin",
			ExpiresAt: &exp,
			MaxUses:   &maxUses,
			Uses:      1,
			Revoked:   false,
			RedeemedBy: []peligrosa.Redemption{
				{Username: "alice", JellyfinID: "jf-abc", RedeemedAt: time.Now().UTC()},
			},
		},
	}
	data, _ := json.Marshal(invites)
	os.WriteFile(path, data, 0600)

	migrateInvitesJSON(db, path)

	// File renamed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected invites.json to be renamed")
	}

	// Verify invite in DB.
	store := peligrosa.NewInviteStore(db, nil)
	list := store.ListInvites()
	if len(list) != 1 {
		t.Fatalf("expected 1 invite, got %d", len(list))
	}
	if list[0].Token != tok {
		t.Errorf("token mismatch: got %q", list[0].Token)
	}
	if list[0].Label != "Test label" {
		t.Errorf("label mismatch: got %q", list[0].Label)
	}
	if list[0].Uses != 1 {
		t.Errorf("uses = %d, want 1", list[0].Uses)
	}
	if len(list[0].RedeemedBy) != 1 {
		t.Errorf("redemptions = %d, want 1", len(list[0].RedeemedBy))
	}
}

// ── migrateRequestsJSON ───────────────────────────────────────────────────────

func TestMigrateRequestsJSON_NoFile(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	migrateRequestsJSON(db, filepath.Join(dir, "requests.json"))
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 requests, got %d", count)
	}
}

func TestMigrateRequestsJSON_Inserts(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.json")

	now := time.Now().UTC()
	requests := []*peligrosa.MediaRequest{
		{
			ID:          "req_migrate_001",
			Type:        "movie",
			TmdbID:      42,
			Title:       "Test Film",
			Year:        2024,
			RequestedBy: "bob",
			State:       peligrosa.RequestPending,
			CreatedAt:   now,
			UpdatedAt:   now,
			History: []peligrosa.RequestEvent{
				{At: now, State: peligrosa.RequestPending, Actor: "bob"},
			},
		},
	}
	data, _ := json.Marshal(requests)
	os.WriteFile(path, data, 0600)

	migrateRequestsJSON(db, path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected requests.json to be renamed")
	}

	store := peligrosa.NewRequestStore(reporeqs.New(db), &migrateTestFulfiller{})
	all := store.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 request, got %d", len(all))
	}
	if all[0].TmdbID != 42 {
		t.Errorf("TmdbID = %d, want 42", all[0].TmdbID)
	}
	if len(all[0].History) != 1 {
		t.Errorf("history events = %d, want 1", len(all[0].History))
	}
}
