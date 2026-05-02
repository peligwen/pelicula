package migratejson

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func (f *migrateTestFulfiller) AddMovie(_ context.Context, _, _ int, _ string) (int, error) {
	return 0, nil
}
func (f *migrateTestFulfiller) AddSeries(_ context.Context, _, _ int, _ string) (int, error) {
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
		`CREATE TABLE IF NOT EXISTS migrated_json_files (
			filename    TEXT PRIMARY KEY,
			migrated_at TEXT NOT NULL
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
	migrateRolesJSON(context.Background(), db, filepath.Join(dir, "roles.json"))
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

	migrateRolesJSON(context.Background(), db, path)

	// File should be renamed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected roles.json to be renamed to .migrated")
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Errorf("expected .migrated file: %v", err)
	}

	// Verify rows in DB.
	store := peligrosa.NewRolesStore(db)
	role, ok := store.Lookup(context.Background(), "jf-001")
	if !ok || role != peligrosa.RoleAdmin {
		t.Errorf("Lookup jf-001: role=%q ok=%v, want admin/true", role, ok)
	}
	role, ok = store.Lookup(context.Background(), "jf-002")
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
	migrateRolesJSON(context.Background(), db, path)
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
	migrateInvitesJSON(context.Background(), db, filepath.Join(dir, "invites.json"))
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

	migrateInvitesJSON(context.Background(), db, path)

	// File renamed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected invites.json to be renamed")
	}

	// Verify invite in DB.
	store := peligrosa.NewInviteStore(db, nil)
	list := store.ListInvites(context.Background())
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

// TestMigrateInvitesJSON_DoubleRunNoDuplicates verifies that when markMigrated
// fails after a successful commit, re-running the migration produces no
// duplicate child rows because migrated_json_files short-circuits the second run.
func TestMigrateInvitesJSON_DoubleRunNoDuplicates(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "invites.json")

	tok := "aaaabbbbccccddddeeeeffffgggghhhhiiijjjkklll"[:43]
	invites := []peligrosa.Invite{
		{
			Token:     tok,
			Label:     "dup test",
			CreatedAt: time.Now().UTC(),
			CreatedBy: "admin",
			Uses:      1,
			RedeemedBy: []peligrosa.Redemption{
				{Username: "alice", JellyfinID: "jf-dup", RedeemedAt: time.Now().UTC()},
			},
		},
	}
	data, _ := json.Marshal(invites)
	os.WriteFile(path, data, 0600)

	// First run: override markMigrated to simulate rename failure.
	orig := markMigrated
	markMigrated = func(string) error { return errors.New("simulated rename failure") }
	if err := migrateInvitesJSON(context.Background(), db, path); err != nil {
		t.Fatalf("first run returned error: %v", err)
	}
	markMigrated = orig

	// Data must have committed.
	var redemptionCount int
	db.QueryRow(`SELECT COUNT(*) FROM redemptions WHERE invite_token = ?`, tok).Scan(&redemptionCount)
	if redemptionCount != 1 {
		t.Fatalf("after first run: want 1 redemption, got %d", redemptionCount)
	}
	// migrated_json_files row must exist (commit succeeded).
	var tracked int
	db.QueryRow(`SELECT COUNT(*) FROM migrated_json_files WHERE filename = ?`, path).Scan(&tracked)
	if tracked != 1 {
		t.Fatalf("after first run: migrated_json_files row missing")
	}
	// File must still exist (rename failed).
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("after first run: file unexpectedly gone despite rename failure")
	}

	// Second run: markMigrated is restored. The file still exists on disk, but
	// migrated_json_files already has the row — migration must short-circuit.
	if err := migrateInvitesJSON(context.Background(), db, path); err != nil {
		t.Fatalf("second run returned error: %v", err)
	}

	// Redemption count must be unchanged — no duplicates.
	db.QueryRow(`SELECT COUNT(*) FROM redemptions WHERE invite_token = ?`, tok).Scan(&redemptionCount)
	if redemptionCount != 1 {
		t.Errorf("after second run: want 1 redemption (no duplicates), got %d", redemptionCount)
	}
}

// TestMigrateRequestsJSON_DoubleRunNoDuplicates is the same guarantee for
// request_events.
func TestMigrateRequestsJSON_DoubleRunNoDuplicates(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.json")

	now := time.Now().UTC()
	requests := []*peligrosa.MediaRequest{
		{
			ID:          "req_nodup_001",
			Type:        "movie",
			TmdbID:      99,
			Title:       "Dup Film",
			Year:        2025,
			RequestedBy: "carol",
			State:       peligrosa.RequestPending,
			CreatedAt:   now,
			UpdatedAt:   now,
			History: []peligrosa.RequestEvent{
				{At: now, State: peligrosa.RequestPending, Actor: "carol"},
			},
		},
	}
	data, _ := json.Marshal(requests)
	os.WriteFile(path, data, 0600)

	// First run with rename failure.
	orig := markMigrated
	markMigrated = func(string) error { return errors.New("simulated rename failure") }
	if err := migrateRequestsJSON(context.Background(), db, path); err != nil {
		t.Fatalf("first run returned error: %v", err)
	}
	markMigrated = orig

	var eventCount int
	db.QueryRow(`SELECT COUNT(*) FROM request_events WHERE request_id = ?`, "req_nodup_001").Scan(&eventCount)
	if eventCount != 1 {
		t.Fatalf("after first run: want 1 event, got %d", eventCount)
	}

	// Second run must short-circuit.
	if err := migrateRequestsJSON(context.Background(), db, path); err != nil {
		t.Fatalf("second run returned error: %v", err)
	}

	db.QueryRow(`SELECT COUNT(*) FROM request_events WHERE request_id = ?`, "req_nodup_001").Scan(&eventCount)
	if eventCount != 1 {
		t.Errorf("after second run: want 1 event (no duplicates), got %d", eventCount)
	}
}

// ── migrateRequestsJSON ───────────────────────────────────────────────────────

func TestMigrateRequestsJSON_NoFile(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	migrateRequestsJSON(context.Background(), db, filepath.Join(dir, "requests.json"))
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

	migrateRequestsJSON(context.Background(), db, path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected requests.json to be renamed")
	}

	store := peligrosa.NewRequestStore(reporeqs.New(db), &migrateTestFulfiller{})
	all := store.All(context.Background())
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

// TestRun_AllThreeMigrate verifies that Run processes all three file types and
// short-circuits on second invocation (no duplicates, no errors).
func TestRun_AllThreeMigrate(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()

	now := time.Now().UTC()
	tok := "aaaabbbbccccddddeeeeffffgggghhhhiiijjjkklll"[:43]

	// Write all three JSON files.
	rolesData, _ := json.Marshal(peligrosa.RolesFile{
		Version: 1,
		Users:   []peligrosa.RolesEntry{{JellyfinID: "jf-r01", Username: "dave", Role: peligrosa.RoleViewer}},
	})
	os.WriteFile(filepath.Join(dir, "roles.json"), rolesData, 0600)

	invitesData, _ := json.Marshal([]peligrosa.Invite{{
		Token: tok, Label: "run-test", CreatedAt: now, CreatedBy: "admin", Uses: 0,
	}})
	os.WriteFile(filepath.Join(dir, "invites.json"), invitesData, 0600)

	reqsData, _ := json.Marshal([]*peligrosa.MediaRequest{{
		ID: "req_run_001", Type: "movie", TmdbID: 7, Title: "Run Film",
		Year: 2025, RequestedBy: "eve", State: peligrosa.RequestPending,
		CreatedAt: now, UpdatedAt: now,
	}})
	os.WriteFile(filepath.Join(dir, "requests.json"), reqsData, 0600)

	if err := Run(context.Background(), db, dir); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var roleCount, inviteCount, reqCount int
	db.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&roleCount)
	db.QueryRow(`SELECT COUNT(*) FROM invites`).Scan(&inviteCount)
	db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&reqCount)
	if roleCount != 1 || inviteCount != 1 || reqCount != 1 {
		t.Errorf("after first Run: roles=%d invites=%d requests=%d, want all 1", roleCount, inviteCount, reqCount)
	}

	// Second Run — files are renamed so they won't exist, but migrated_json_files
	// rows also exist. Either path produces no duplicates.
	if err := Run(context.Background(), db, dir); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&roleCount)
	db.QueryRow(`SELECT COUNT(*) FROM invites`).Scan(&inviteCount)
	db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&reqCount)
	if roleCount != 1 || inviteCount != 1 || reqCount != 1 {
		t.Errorf("after second Run: roles=%d invites=%d requests=%d, want all still 1", roleCount, inviteCount, reqCount)
	}
}
