package roles_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
	"pelicula-api/internal/repo/roles"
)

// newTestDB opens an in-memory SQLite database with the roles table created.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE roles (
		jellyfin_id TEXT PRIMARY KEY,
		username    TEXT NOT NULL,
		role        TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStore_IsEmpty(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	if !s.IsEmpty() {
		t.Error("fresh store should be empty")
	}
	if err := s.Upsert("id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if s.IsEmpty() {
		t.Error("store with one entry should not be empty")
	}
}

func TestStore_Lookup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		seed       []roles.Entry // rows to insert before lookup
		jellyfinID string
		wantRole   string
		wantFound  bool
	}{
		{
			name:       "not found",
			seed:       nil,
			jellyfinID: "missing",
			wantRole:   "",
			wantFound:  false,
		},
		{
			name:       "found viewer",
			seed:       []roles.Entry{{JellyfinID: "jf-viewer", Username: "viewer-user", Role: "viewer"}},
			jellyfinID: "jf-viewer",
			wantRole:   "viewer",
			wantFound:  true,
		},
		{
			name:       "found admin",
			seed:       []roles.Entry{{JellyfinID: "jf-admin", Username: "admin-user", Role: "admin"}},
			jellyfinID: "jf-admin",
			wantRole:   "admin",
			wantFound:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := roles.New(newTestDB(t))
			for _, e := range tc.seed {
				if err := s.Upsert(e.JellyfinID, e.Username, e.Role); err != nil {
					t.Fatalf("seed Upsert: %v", err)
				}
			}
			got, found := s.Lookup(tc.jellyfinID)
			if found != tc.wantFound {
				t.Errorf("Lookup(%q) found = %v, want %v", tc.jellyfinID, found, tc.wantFound)
			}
			if got != tc.wantRole {
				t.Errorf("Lookup(%q) role = %q, want %q", tc.jellyfinID, got, tc.wantRole)
			}
		})
	}
}

func TestStore_Upsert_Insert(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert("id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	role, ok := s.Lookup("id1")
	if !ok || role != "viewer" {
		t.Errorf("after insert: got (%q, %v), want (viewer, true)", role, ok)
	}
}

func TestStore_Upsert_Update(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert("id1", "alice", "viewer"); err != nil {
		t.Fatalf("initial Upsert: %v", err)
	}
	if err := s.Upsert("id1", "alice", "admin"); err != nil {
		t.Fatalf("update Upsert: %v", err)
	}
	role, ok := s.Lookup("id1")
	if !ok || role != "admin" {
		t.Errorf("after update: got (%q, %v), want (admin, true)", role, ok)
	}
	// Entry count must not grow on update.
	if count := len(s.All()); count != 1 {
		t.Errorf("expected 1 entry after upsert-update, got %d", count)
	}
}

func TestStore_Upsert_UsernameRefresh(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert("id1", "old-name", "viewer"); err != nil {
		t.Fatalf("initial Upsert: %v", err)
	}
	if err := s.Upsert("id1", "new-name", "viewer"); err != nil {
		t.Fatalf("update Upsert: %v", err)
	}
	entries := s.All()
	if len(entries) != 1 || entries[0].Username != "new-name" {
		t.Errorf("expected username=new-name, got %v", entries)
	}
}

func TestStore_All(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	// Empty store returns empty (not nil) slice.
	got := s.All()
	if got == nil {
		t.Error("All() on empty store returned nil, want []Entry{}")
	}
	if len(got) != 0 {
		t.Errorf("All() on empty store returned %d entries, want 0", len(got))
	}

	if err := s.Upsert("id2", "bob", "manager"); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}
	if err := s.Upsert("id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert alice: %v", err)
	}

	got = s.All()
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	// Results are ordered by username, so alice first.
	if got[0].Username != "alice" || got[0].Role != "viewer" || got[0].JellyfinID != "id1" {
		t.Errorf("entry[0] = %+v, want alice/viewer/id1", got[0])
	}
	if got[1].Username != "bob" || got[1].Role != "manager" || got[1].JellyfinID != "id2" {
		t.Errorf("entry[1] = %+v, want bob/manager/id2", got[1])
	}
}

func TestStore_Delete(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert("id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, found := s.Lookup("id1"); !found {
		t.Fatal("expected entry after upsert")
	}
	if err := s.Delete("id1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found := s.Lookup("id1"); found {
		t.Fatal("expected entry gone after delete")
	}
	// Deleting a non-existent ID should not error.
	if err := s.Delete("no-such-id"); err != nil {
		t.Fatalf("delete non-existent: %v", err)
	}
}

func TestStore_RoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	if !s.IsEmpty() {
		t.Error("fresh store should be empty")
	}

	if err := s.Upsert("id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert id1: %v", err)
	}
	if err := s.Upsert("id2", "bob", "manager"); err != nil {
		t.Fatalf("Upsert id2: %v", err)
	}

	// A second Store on the same DB should see the same data.
	s2 := roles.New(db)
	role, ok := s2.Lookup("id1")
	if !ok || role != "viewer" {
		t.Errorf("id1: got (%q, %v), want (viewer, true)", role, ok)
	}
	role, ok = s2.Lookup("id2")
	if !ok || role != "manager" {
		t.Errorf("id2: got (%q, %v), want (manager, true)", role, ok)
	}

	// Upsert update via s2.
	if err := s2.Upsert("id1", "alice", "admin"); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	role, _ = s2.Lookup("id1")
	if role != "admin" {
		t.Errorf("after update: id1 role = %q, want admin", role)
	}
	// Entry count must not grow on update.
	if count := len(s2.All()); count != 2 {
		t.Errorf("expected 2 entries after upsert-update, got %d", count)
	}

	// Unknown ID.
	if _, ok := s2.Lookup("unknown"); ok {
		t.Error("Lookup of unknown ID should return false")
	}
}
