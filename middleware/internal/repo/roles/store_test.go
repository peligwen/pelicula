package roles_test

import (
	"context"
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
	ctx := context.Background()
	db := newTestDB(t)
	s := roles.New(db)

	if !s.IsEmpty(ctx) {
		t.Error("fresh store should be empty")
	}
	if err := s.Upsert(ctx, "id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if s.IsEmpty(ctx) {
		t.Error("store with one entry should not be empty")
	}
}

func TestStore_Lookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

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
				if err := s.Upsert(ctx, e.JellyfinID, e.Username, e.Role); err != nil {
					t.Fatalf("seed Upsert: %v", err)
				}
			}
			got, found := s.Lookup(ctx, tc.jellyfinID)
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
	ctx := context.Background()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert(ctx, "id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	role, ok := s.Lookup(ctx, "id1")
	if !ok || role != "viewer" {
		t.Errorf("after insert: got (%q, %v), want (viewer, true)", role, ok)
	}
}

func TestStore_Upsert_Update(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert(ctx, "id1", "alice", "viewer"); err != nil {
		t.Fatalf("initial Upsert: %v", err)
	}
	if err := s.Upsert(ctx, "id1", "alice", "admin"); err != nil {
		t.Fatalf("update Upsert: %v", err)
	}
	role, ok := s.Lookup(ctx, "id1")
	if !ok || role != "admin" {
		t.Errorf("after update: got (%q, %v), want (admin, true)", role, ok)
	}
	// Entry count must not grow on update.
	entries, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after upsert-update, got %d", len(entries))
	}
}

func TestStore_Upsert_UsernameRefresh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert(ctx, "id1", "old-name", "viewer"); err != nil {
		t.Fatalf("initial Upsert: %v", err)
	}
	if err := s.Upsert(ctx, "id1", "new-name", "viewer"); err != nil {
		t.Fatalf("update Upsert: %v", err)
	}
	entries, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(entries) != 1 || entries[0].Username != "new-name" {
		t.Errorf("expected username=new-name, got %v", entries)
	}
}

func TestStore_All(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := roles.New(db)

	// Empty store returns empty (not nil) slice.
	got, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All on empty store: %v", err)
	}
	if got == nil {
		t.Error("All() on empty store returned nil, want []Entry{}")
	}
	if len(got) != 0 {
		t.Errorf("All() on empty store returned %d entries, want 0", len(got))
	}

	if err := s.Upsert(ctx, "id2", "bob", "manager"); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}
	if err := s.Upsert(ctx, "id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert alice: %v", err)
	}

	got, err = s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
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
	ctx := context.Background()
	db := newTestDB(t)
	s := roles.New(db)

	if err := s.Upsert(ctx, "id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, found := s.Lookup(ctx, "id1"); !found {
		t.Fatal("expected entry after upsert")
	}
	if err := s.Delete(ctx, "id1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found := s.Lookup(ctx, "id1"); found {
		t.Fatal("expected entry gone after delete")
	}
	// Deleting a non-existent ID should not error.
	if err := s.Delete(ctx, "no-such-id"); err != nil {
		t.Fatalf("delete non-existent: %v", err)
	}
}

func TestStore_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := roles.New(db)

	if !s.IsEmpty(ctx) {
		t.Error("fresh store should be empty")
	}

	if err := s.Upsert(ctx, "id1", "alice", "viewer"); err != nil {
		t.Fatalf("Upsert id1: %v", err)
	}
	if err := s.Upsert(ctx, "id2", "bob", "manager"); err != nil {
		t.Fatalf("Upsert id2: %v", err)
	}

	// A second Store on the same DB should see the same data.
	s2 := roles.New(db)
	role, ok := s2.Lookup(ctx, "id1")
	if !ok || role != "viewer" {
		t.Errorf("id1: got (%q, %v), want (viewer, true)", role, ok)
	}
	role, ok = s2.Lookup(ctx, "id2")
	if !ok || role != "manager" {
		t.Errorf("id2: got (%q, %v), want (manager, true)", role, ok)
	}

	// Upsert update via s2.
	if err := s2.Upsert(ctx, "id1", "alice", "admin"); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	role, _ = s2.Lookup(ctx, "id1")
	if role != "admin" {
		t.Errorf("after update: id1 role = %q, want admin", role)
	}
	// Entry count must not grow on update.
	entries, err := s2.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries after upsert-update, got %d", len(entries))
	}

	// Unknown ID.
	if _, ok := s2.Lookup(ctx, "unknown"); ok {
		t.Error("Lookup of unknown ID should return false")
	}
}

// TestStore_RespectsContextCancel pins that all methods propagate context
// cancellation to the underlying SQL driver. A pre-canceled ctx must cause
// ExecContext to return a non-nil error.
func TestStore_RespectsContextCancel(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	if err := s.Upsert(ctx, "id1", "alice", "viewer"); err == nil {
		t.Error("Upsert with canceled ctx: expected non-nil error, got nil")
	}
}

// TestStore_AllRespectsContextCancel pins that All propagates context
// cancellation to QueryContext.
func TestStore_AllRespectsContextCancel(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	s := roles.New(db)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.All(ctx)
	if err == nil {
		t.Error("All with canceled ctx: expected non-nil error, got nil")
	}
}
