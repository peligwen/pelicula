package peligrosa

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testJellyfinID is a valid 32-char hex Jellyfin user ID for role-change tests.
const testJellyfinID = "aabbccddeeff00112233445566778899"

// newOperatorTestAuth builds an Auth with a real (test-DB-backed) roles store
// and sessions store, pre-seeded with a role entry for testJellyfinID/username,
// plus a live session (both in-memory and DB-backed) for that username. Returns
// the Auth and the session token.
func newOperatorTestAuth(t *testing.T, username string, role UserRole) (*Auth, string) {
	t.Helper()
	a := newTestJellyfinAuth(t, nil, nil)
	if err := a.rolesStore.Upsert(context.Background(), testJellyfinID, username, role); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	token := insertSession(a, username, role, time.Now().Add(time.Hour))
	if err := a.sessionsStore.Create(context.Background(), token, username, string(role), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed DB session: %v", err)
	}
	return a, token
}

// sessionExists reports whether token is present in both the in-memory map
// and the DB-backed sessions store.
func sessionExists(t *testing.T, a *Auth, token string) bool {
	t.Helper()
	a.mu.RLock()
	_, inMem := a.sessions[token]
	a.mu.RUnlock()

	dbSess, err := a.sessionsStore.Lookup(context.Background(), token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	inDB := dbSess != nil
	if inMem != inDB {
		t.Fatalf("in-memory/DB session state disagree: inMem=%v inDB=%v", inMem, inDB)
	}
	return inMem
}

func TestHandleOperatorsGetNilStore(t *testing.T) {
	// (*Auth)(nil) receiver — HandleOperators must return []
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/operators", nil)
	w := httptest.NewRecorder()
	(*Auth)(nil).HandleOperators(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Must be a JSON array (even if empty), not null
	body := w.Body.String()
	if body == "null\n" || body == "null" {
		t.Error("expected [] not null")
	}
}

// ── MWD-1: role change / delete must revoke live sessions ───────────────────

func TestHandleOperatorsWithID_Demote_RevokesSessions(t *testing.T) {
	a, token := newOperatorTestAuth(t, "alice", RoleAdmin)
	if !sessionExists(t, a, token) {
		t.Fatal("sanity check: seeded session should exist before the demote")
	}

	body, _ := json.Marshal(map[string]string{"role": "viewer", "username": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/operators/"+testJellyfinID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.HandleOperatorsWithID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if sessionExists(t, a, token) {
		t.Error("demoted user's session should have been revoked (in-memory and DB)")
	}
	role, ok := a.rolesStore.Lookup(context.Background(), testJellyfinID)
	if !ok || role != RoleViewer {
		t.Errorf("stored role = (%q, %v), want (viewer, true)", role, ok)
	}
}

func TestHandleOperatorsWithID_Promote_DoesNotRevokeSessions(t *testing.T) {
	a, token := newOperatorTestAuth(t, "alice", RoleViewer)
	if !sessionExists(t, a, token) {
		t.Fatal("sanity check: seeded session should exist before the promote")
	}

	body, _ := json.Marshal(map[string]string{"role": "admin", "username": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/operators/"+testJellyfinID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.HandleOperatorsWithID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !sessionExists(t, a, token) {
		t.Error("promoted user's existing session should NOT be revoked — it should not force re-login")
	}
	role, ok := a.rolesStore.Lookup(context.Background(), testJellyfinID)
	if !ok || role != RoleAdmin {
		t.Errorf("stored role = (%q, %v), want (admin, true)", role, ok)
	}
}

func TestHandleOperatorsWithID_SameRole_DoesNotRevokeSessions(t *testing.T) {
	// A no-op "change" (re-submitting the same role) must not be treated as a
	// downgrade — roleRank(new) < roleRank(old) is false when they're equal.
	a, token := newOperatorTestAuth(t, "alice", RoleManager)

	body, _ := json.Marshal(map[string]string{"role": "manager", "username": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/operators/"+testJellyfinID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.HandleOperatorsWithID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !sessionExists(t, a, token) {
		t.Error("re-submitting the same role should not revoke sessions")
	}
}

func TestHandleOperatorsWithID_Delete_RevokesSessions(t *testing.T) {
	a, token := newOperatorTestAuth(t, "alice", RoleManager)
	if !sessionExists(t, a, token) {
		t.Fatal("sanity check: seeded session should exist before the delete")
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/operators/"+testJellyfinID, nil)
	w := httptest.NewRecorder()
	a.HandleOperatorsWithID(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
	if sessionExists(t, a, token) {
		t.Error("deleted operator's session should have been revoked (in-memory and DB)")
	}
	if _, ok := a.rolesStore.Lookup(context.Background(), testJellyfinID); ok {
		t.Error("role entry should have been deleted")
	}
}

func TestHandleOperatorsWithID_Delete_UnknownID_NoPanic(t *testing.T) {
	// Deleting an ID with no roles-table entry must not panic when looking up
	// the (nonexistent) username to revoke — hadPrev must gate the revoke call.
	a := newTestJellyfinAuth(t, nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/operators/"+testJellyfinID, nil)
	w := httptest.NewRecorder()
	a.HandleOperatorsWithID(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (delete of unknown ID is a no-op)", w.Code)
	}
}

func TestHandleOperatorsWithID_InvalidRole(t *testing.T) {
	// 32-char dashless hex — valid Jellyfin ID format
	body, _ := json.Marshal(map[string]string{"role": "superadmin", "username": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/operators/aabbccddeeff00112233445566778899", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	(*Auth)(nil).HandleOperatorsWithID(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
