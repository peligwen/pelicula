package peligrosa_test

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
	"pelicula-api/internal/peligrosa"
)

func newTestRolesDB(t *testing.T) *sql.DB {
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

func TestRolesStoreDelete(t *testing.T) {
	ctx := context.Background()
	db := newTestRolesDB(t)
	rs := peligrosa.NewRolesStore(db)

	if err := rs.Upsert(ctx, "user-1", "alice", peligrosa.RoleViewer); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, found := rs.Lookup(ctx, "user-1"); !found {
		t.Fatal("expected entry after upsert")
	}
	if err := rs.Delete(ctx, "user-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found := rs.Lookup(ctx, "user-1"); found {
		t.Fatal("expected entry gone after delete")
	}
	// Deleting a non-existent ID should not error
	if err := rs.Delete(ctx, "no-such-id"); err != nil {
		t.Fatalf("delete non-existent: %v", err)
	}
}
