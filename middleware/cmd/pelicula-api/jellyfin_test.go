package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"pelicula-api/httputil"
	"pelicula-api/internal/peligrosa"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJellyfinAuth registers a POST /Users/AuthenticateByName handler that
// always succeeds with a static access token.
func fakeJellyfinAuth(mux *http.ServeMux) {
	mux.HandleFunc("/Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"AccessToken":"test-token"}`))
	})
}

// newFakeJellyfin starts an httptest.Server, applies fakeJellyfinAuth, then
// runs the provided setup func to add test-specific handlers.
// It also overrides the package-level jellyfinURL for the duration of the test.
func newFakeJellyfin(t *testing.T, setup func(mux *http.ServeMux)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	fakeJellyfinAuth(mux)
	if setup != nil {
		setup(mux)
	}
	srv := httptest.NewServer(mux)
	orig := jellyfinURL
	jellyfinURL = srv.URL
	t.Cleanup(func() {
		srv.Close()
		jellyfinURL = orig
	})
	// Wire authMiddleware with a JellyfinClient that can call CreateUser on the
	// fake server. We build a minimal ServiceClients with a test API key so that
	// jellyfinAuth() returns immediately without touching the filesystem, then
	// pass srv.Client() so all HTTP goes to the test server.
	// Tests that need a specific mode or store assign authMiddleware themselves
	// after calling newFakeJellyfin.
	testSvcs := NewServiceClients(t.TempDir())
	testSvcs.JellyfinAPIKey = "test-token"
	origAuth := authMiddleware
	authMiddleware = peligrosa.NewAuth(peligrosa.AuthConfig{
		DB:       testDB(t),
		Jellyfin: NewJellyfinHTTPClient(srv.Client(), testSvcs),
	})
	t.Cleanup(func() { authMiddleware = origAuth })
	return srv
}

// resetServices points the global services at a dummy config dir so calls don't
// panic trying to open real files. jellyfinURL is already overridden by newFakeJellyfin.
func resetServices(t *testing.T) {
	t.Helper()
	orig := services
	services = NewServiceClients(t.TempDir())
	// Set a test API key so jellyfinAuth() returns it directly
	// without trying to read /project/.env.
	services.JellyfinAPIKey = "test-token"
	t.Cleanup(func() { services = orig })
}

// ── GET /api/pelicula/users ──────────────────────────────────────────────────

func TestHandleUsers_GetHappyPath(t *testing.T) {
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Name":"alice","Id":"id1","HasPassword":true,"LastLoginDate":"2026-01-01T00:00:00Z"}]`))
		})
	})
	resetServices(t)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/users", nil)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var users []JellyfinUser
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("want 1 user, got %d", len(users))
	}
	if users[0].Name != "alice" {
		t.Errorf("Name = %q, want alice", users[0].Name)
	}
	if !users[0].HasPassword {
		t.Error("HasPassword should be true")
	}
	if users[0].LastLoginDate == "" {
		t.Error("LastLoginDate should be populated")
	}
}

func TestHandleUsers_GetJellyfinFailure(t *testing.T) {
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		})
	})
	resetServices(t)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/users", nil)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	// Error body must not leak Jellyfin internals.
	if strings.Contains(w.Body.String(), "internal error") {
		t.Error("response body leaked Jellyfin error details")
	}
}

// ── POST /api/pelicula/users ─────────────────────────────────────────────────

func TestHandleUsers_PostHappyPath(t *testing.T) {
	const testUserID = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + testUserID + `"}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			// Handles /Users/{id}/Password and /Users/{id} DELETE
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	body := strings.NewReader(`{"username":"bob","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body)
	}
}

func TestHandleUsers_PostEmptyPassword(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	body := strings.NewReader(`{"username":"bob","password":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsers_PostMissingUsername(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	body := strings.NewReader(`{"username":"","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsers_PostInvalidJSON(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsers_PostMethodNotAllowed(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/pelicula/users", nil)
			w := httptest.NewRecorder()
			handleUsers(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", w.Code)
			}
		})
	}
}

func TestHandleUsers_PostOversizedBody(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	// 2 MB of JSON-ish garbage — well over the 1 MB cap.
	giant := strings.Repeat(`{"username":"x","password":"`, 80_000) + `"}` // ~2.2 MB
	body := strings.NewReader(giant)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	// MaxBytesReader causes Decode to fail → 400, not 500/502.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized body", w.Code)
	}
}

func TestHandleUsers_PostForeignOrigin(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	body := strings.NewReader(`{"username":"bob","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginSoft(http.HandlerFunc(handleUsers)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleUsers_UsernameValidation(t *testing.T) {
	cases := []struct {
		name     string
		username string
	}{
		{"too long", strings.Repeat("a", 65)},
		{"leading space", " bob"},
		{"trailing space", "bob "},
		{"tab char", "bo\tb"},
		{"newline", "bo\nb"},
		{"forward slash", "bo/b"},
		{"backslash", `bo\b`},
		{"control char", "bo\x01b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			newFakeJellyfin(t, nil)
			resetServices(t)

			payload, _ := json.Marshal(map[string]string{"username": c.username, "password": "hunter2"})
			req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", strings.NewReader(string(payload)))
			w := httptest.NewRecorder()
			handleUsers(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("username %q: status = %d, want 400", c.username, w.Code)
			}
		})
	}
}

// ── CreateJellyfinUser rollback ───────────────────────────────────────────────

func TestCreateJellyfinUser_PasswordSetFailsRollbackSucceeds(t *testing.T) {
	var deleteCalls atomic.Int32
	const rollbackID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + rollbackID + `"}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				deleteCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// POST /Users/{id}/Password → fail
			http.Error(w, "cannot set password", http.StatusInternalServerError)
		})
	})
	resetServices(t)

	_, err := CreateJellyfinUser(services, "alice", "secret")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "user was removed") {
		t.Errorf("error = %q, want mention of 'user was removed'", err.Error())
	}
	if deleteCalls.Load() != 1 {
		t.Errorf("DELETE called %d times, want 1", deleteCalls.Load())
	}
}

func TestCreateJellyfinUser_PasswordSetFailsRollbackAlsoFails(t *testing.T) {
	const badID = "aaaaaaaa-bbbb-cccc-dddd-ffffffffffff"
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + badID + `"}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			// Both /Password POST and DELETE fail.
			http.Error(w, "server error", http.StatusInternalServerError)
		})
	})
	resetServices(t)

	_, err := CreateJellyfinUser(services, "alice", "secret")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rollback failed") {
		t.Errorf("error = %q, want mention of 'rollback failed'", err.Error())
	}
	if !strings.Contains(err.Error(), "alice") {
		t.Errorf("error = %q, should mention username 'alice' for manual cleanup", err.Error())
	}
}

func TestCreateJellyfinUser_NoIdInResponse(t *testing.T) {
	var passwordCalls atomic.Int32

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			// Missing Id field.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			passwordCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	_, err := CreateJellyfinUser(services, "alice", "secret")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no user ID") {
		t.Errorf("error = %q, want 'no user ID'", err.Error())
	}
	if passwordCalls.Load() != 0 {
		t.Errorf("/Password called %d times after missing ID, want 0", passwordCalls.Load())
	}
}

func TestCreateJellyfinUser_JellyfinCreateFailure(t *testing.T) {
	var deleteCalls atomic.Int32

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "conflict", http.StatusBadRequest)
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			deleteCalls.Add(1)
		})
	})
	resetServices(t)

	_, err := CreateJellyfinUser(services, "alice", "secret")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if deleteCalls.Load() != 0 {
		t.Errorf("DELETE called %d times when create itself failed, want 0", deleteCalls.Load())
	}
}

func TestCreateJellyfinUser_JellyfinBadRequestMapsTo400(t *testing.T) {
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "name taken", http.StatusBadRequest)
		})
	})
	resetServices(t)

	body := strings.NewReader(`{"username":"existing","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when Jellyfin rejects name", w.Code)
	}
}

func TestCreateJellyfinUser_NonUUIDIdRejected(t *testing.T) {
	// Jellyfin returns an id that is not a UUID (e.g. a path-traversal string).
	// The handler must reject it before building any URL path.
	var passwordCalls atomic.Int32

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Non-UUID id — should be rejected before any /Password call.
			w.Write([]byte(`{"Id":"../System/Shutdown"}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			passwordCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	_, err := CreateJellyfinUser(services, "alice", "secret")
	if err == nil {
		t.Fatal("expected error for non-UUID id, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected user ID format") {
		t.Errorf("error = %q, want 'unexpected user ID format'", err.Error())
	}
	if passwordCalls.Load() != 0 {
		t.Errorf("/Password called %d times after rejecting bad id, want 0", passwordCalls.Load())
	}
}

// ── validUsername ─────────────────────────────────────────────────────────────

func TestValidUsername(t *testing.T) {
	valid := []string{
		"alice",
		"bob123",
		"user name", // internal space is OK
		strings.Repeat("a", 64),
		"café",
	}
	invalid := []string{
		"",
		strings.Repeat("a", 65),
		" leading",
		"trailing ",
		"tab\there",
		"new\nline",
		"path/sep",
		`back\slash`,
		"ctrl\x01char",
	}
	for _, v := range valid {
		if !validUsername(v) {
			t.Errorf("validUsername(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if validUsername(v) {
			t.Errorf("validUsername(%q) = true, want false", v)
		}
	}
}

// ── ErrPasswordRequired sentinel ──────────────────────────────────────────────

func TestCreateJellyfinUser_EmptyPasswordReturnsSentinel(t *testing.T) {
	// No fake server needed — the check fires before any HTTP call.
	origSvcs := services
	services = NewServiceClients(t.TempDir())
	t.Cleanup(func() { services = origSvcs })

	_, err := CreateJellyfinUser(services, "alice", "")
	if !isErrPasswordRequired(err) {
		t.Errorf("expected ErrPasswordRequired, got %v", err)
	}
}

// isErrPasswordRequired is a test helper that mirrors the errors.Is check in handleUsers.
func isErrPasswordRequired(err error) bool {
	return err != nil && err.Error() == ErrPasswordRequired.Error()
}

// ── Session / auth wiring sanity ──────────────────────────────────────────────

// TestHandleUsers_NilAuthMiddlewareDoesNotPanic ensures that if authMiddleware
// is nil (e.g. in setup mode) the handler falls through without panicking.
func TestHandleUsers_NilAuthMiddlewareDoesNotPanic(t *testing.T) {
	orig := authMiddleware
	authMiddleware = nil
	t.Cleanup(func() { authMiddleware = orig })

	// Should not panic — handleUsers guards against nil authMiddleware before any auth check.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handleUsers panicked with nil authMiddleware: %v", r)
		}
	}()

	body := strings.NewReader(`{"username":"x","password":"y"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	// Will fail at Jellyfin call (no server), which is fine — we just want no panic.
	handleUsers(w, req)
	_ = w.Code
}

// ── validJellyfinID ───────────────────────────────────────────────────────────

func TestValidJellyfinID(t *testing.T) {
	valid := []string{
		"3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21",
		"00000000-0000-0000-0000-000000000000",
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF",
		// Dashless 32-char form: Jellyfin's actual wire format from /Users.
		"a1b2c3d4e5f67890abcdef1234567890",
		"00000000000000000000000000000000",
	}
	invalid := []string{
		"",
		"not-a-uuid",
		"../System/Shutdown",
		"3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f2",   // too short (35)
		"3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f210", // too long (37)
		"3a4d9e71x6a1b-4f2c-9d12-98b4c76e3f21",  // wrong separator
		"a1b2c3d4e5f67890abcdef123456789",       // dashless too short (31)
	}
	for _, v := range valid {
		if !validJellyfinID(v) {
			t.Errorf("validJellyfinID(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if validJellyfinID(v) {
			t.Errorf("validJellyfinID(%q) = true, want false", v)
		}
	}
}

// ── JellyfinUser field mapping ────────────────────────────────────────────────

func TestListJellyfinUsers_FieldMapping(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	payload := `[{"Name":"carol","Id":"cid","HasPassword":false,"LastLoginDate":"` + now + `","Policy":{"IsAdministrator":true}}]`

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(payload))
		})
	})
	resetServices(t)

	users, err := ListJellyfinUsers(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("want 1 user, got %d", len(users))
	}
	u := users[0]
	if u.Name != "carol" {
		t.Errorf("Name = %q, want carol", u.Name)
	}
	if u.ID != "cid" {
		t.Errorf("ID = %q, want cid", u.ID)
	}
	if u.HasPassword {
		t.Error("HasPassword should be false")
	}
	if u.LastLoginDate == "" {
		t.Error("LastLoginDate should be populated")
	}
	if !u.IsAdmin {
		t.Error("IsAdmin should be true when Policy.IsAdministrator is true")
	}
}

// ── DeleteJellyfinUser ────────────────────────────────────────────────────────

func TestDeleteJellyfinUser_HappyPath(t *testing.T) {
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	var deleteCalls atomic.Int32
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				deleteCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		})
	})
	resetServices(t)

	if err := DeleteJellyfinUser(services, uid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleteCalls.Load() != 1 {
		t.Errorf("DELETE called %d times, want 1", deleteCalls.Load())
	}
}

func TestDeleteJellyfinUser_InvalidID(t *testing.T) {
	err := DeleteJellyfinUser(services, "../etc/passwd")
	if err == nil {
		t.Fatal("expected error for invalid ID, got nil")
	}
}

// ── handleUserDelete (last-admin protection) ──────────────────────────────────

const testAdminID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
const testUserID2 = "11111111-2222-3333-4444-555555555555"

// setupUsersHandler registers a /Users handler returning two users: one admin + one regular.
func setupUsersHandler(mux *http.ServeMux, adminID, userID string) {
	mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[` +
			`{"Id":"` + adminID + `","Name":"admin","HasPassword":true,"Policy":{"IsAdministrator":true}},` +
			`{"Id":"` + userID + `","Name":"bob","HasPassword":true,"Policy":{"IsAdministrator":false}}` +
			`]`))
	})
}

func TestHandleUserDelete_HappyPath(t *testing.T) {
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		setupUsersHandler(mux, testAdminID, testUserID2)
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		})
	})
	resetServices(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/"+testUserID2, nil)
	w := httptest.NewRecorder()
	handleUserDelete(w, req, testUserID2)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestHandleUserDelete_LastAdminProtection(t *testing.T) {
	// Only one admin — deleting them should be refused.
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Id":"` + testAdminID + `","Name":"admin","HasPassword":true,"Policy":{"IsAdministrator":true}}]`))
		})
	})
	resetServices(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/"+testAdminID, nil)
	w := httptest.NewRecorder()
	handleUserDelete(w, req, testAdminID)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (last admin protection)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "only admin") {
		t.Errorf("body = %q, want mention of 'only admin'", w.Body)
	}
}

func TestHandleUserDelete_NotFound(t *testing.T) {
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		setupUsersHandler(mux, testAdminID, testUserID2)
	})
	resetServices(t)

	unknownID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/"+unknownID, nil)
	w := httptest.NewRecorder()
	handleUserDelete(w, req, unknownID)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// ── SetJellyfinUserPassword / handleUserPassword ──────────────────────────────

func TestSetJellyfinUserPassword_HappyPath(t *testing.T) {
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	var pwCalls atomic.Int32
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Password") {
				pwCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		})
	})
	resetServices(t)

	if err := SetJellyfinUserPassword(services, uid, "newpassword"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Two POSTs: step 1 clears the password (ResetPassword:true), step 2 sets the new one.
	if pwCalls.Load() != 2 {
		t.Errorf("/Password POST called %d times, want 2", pwCalls.Load())
	}
}

func TestSetJellyfinUserPassword_EmptyPassword(t *testing.T) {
	err := SetJellyfinUserPassword(services, "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21", "")
	if !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("expected ErrPasswordRequired, got %v", err)
	}
}

func TestSetJellyfinUserPassword_InvalidID(t *testing.T) {
	err := SetJellyfinUserPassword(services, "not-a-uuid", "pass")
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestHandleUserPassword_HappyPath(t *testing.T) {
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	body := strings.NewReader(`{"password":"newpass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users/"+uid+"/password", body)
	w := httptest.NewRecorder()
	handleUserPassword(w, req, uid)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestHandleUserPassword_EmptyPassword(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	body := strings.NewReader(`{"password":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users/3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21/password", body)
	w := httptest.NewRecorder()
	handleUserPassword(w, req, "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ── SetJellyfinUserDisabled / handleUsersWithID /disable /enable ──────────────

func TestSetJellyfinUserDisabled(t *testing.T) {
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	var postedBody []byte
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/"+uid, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + uid + `","Name":"alice","Policy":{"IsAdministrator":false,"IsDisabled":false}}`))
		})
		mux.HandleFunc("/Users/"+uid+"/Policy", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var err error
			postedBody, err = io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	if err := SetJellyfinUserDisabled(services, uid, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postedBody == nil {
		t.Fatal("no POST body recorded for /Policy")
	}
	var policy map[string]any
	if err := json.Unmarshal(postedBody, &policy); err != nil {
		t.Fatalf("invalid JSON in POST body: %v", err)
	}
	isDisabled, _ := policy["IsDisabled"].(bool)
	if !isDisabled {
		t.Errorf("policy[IsDisabled] = %v, want true; full body: %s", policy["IsDisabled"], postedBody)
	}
}

// ── SetJellyfinUserLibraryAccess ──────────────────────────────────────────────

func TestSetJellyfinUserLibraryAccess(t *testing.T) {
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	const moviesID = "folder-movies-id-0000000000000000"
	const tvID = "folder-tv-id-000000000000000000"
	var postedBody []byte

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/"+uid, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + uid + `","Name":"alice","Policy":{"IsAdministrator":false,"IsDisabled":false,"EnableAllFolders":true,"EnabledFolders":[]}}`))
		})
		mux.HandleFunc("/Library/VirtualFolders", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Name":"Movies","ItemId":"` + moviesID + `"},{"Name":"TV Shows","ItemId":"` + tvID + `"}]`))
		})
		mux.HandleFunc("/Users/"+uid+"/Policy", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var err error
			postedBody, err = io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	// movies=true, tv=false → should restrict to Movies folder only
	if err := SetJellyfinUserLibraryAccess(services, uid, true, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postedBody == nil {
		t.Fatal("no POST body recorded for /Policy")
	}
	var policy map[string]any
	if err := json.Unmarshal(postedBody, &policy); err != nil {
		t.Fatalf("invalid JSON in POST body: %v", err)
	}
	if enableAll, _ := policy["EnableAllFolders"].(bool); enableAll {
		t.Errorf("policy[EnableAllFolders] = true, want false for partial access")
	}
	folders, _ := policy["EnabledFolders"].([]any)
	if len(folders) != 1 {
		t.Fatalf("EnabledFolders length = %d, want 1; full body: %s", len(folders), postedBody)
	}
	if folders[0].(string) != moviesID {
		t.Errorf("EnabledFolders[0] = %q, want %q", folders[0], moviesID)
	}
}

// ── handleUsersWithID routing ─────────────────────────────────────────────────

func TestHandleUsersWithID_ForeignOrigin(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginSoft(http.HandlerFunc(handleUsersWithID)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleUsersWithID_InvalidID(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/not-a-uuid", nil)
	w := httptest.NewRecorder()
	handleUsersWithID(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsersWithID_PasswordMethodNotAllowed(t *testing.T) {
	newFakeJellyfin(t, nil)
	resetServices(t)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/users/3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21/password", nil)
	w := httptest.NewRecorder()
	handleUsersWithID(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ── GetJellyfinSessions / handleSessions ─────────────────────────────────────

func TestGetJellyfinSessions_HappyPath(t *testing.T) {
	payload := `[
		{"UserName":"alice","DeviceName":"iPhone","Client":"Infuse","LastActivityDate":"2026-04-06T12:00:00Z",
		 "NowPlayingItem":{"Name":"Dune: Part Two","Type":"Movie"}},
		{"UserName":"bob","DeviceName":"TV","Client":"Jellyfin for Android TV","LastActivityDate":"2026-04-06T11:00:00Z"}
	]`
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Sessions", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(payload))
		})
	})
	resetServices(t)

	sessions, err := GetJellyfinSessions(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	if sessions[0].UserName != "alice" {
		t.Errorf("sessions[0].UserName = %q, want alice", sessions[0].UserName)
	}
	if sessions[0].NowPlayingTitle != "Dune: Part Two" {
		t.Errorf("sessions[0].NowPlayingTitle = %q, want 'Dune: Part Two'", sessions[0].NowPlayingTitle)
	}
	if sessions[0].NowPlayingType != "Movie" {
		t.Errorf("sessions[0].NowPlayingType = %q, want Movie", sessions[0].NowPlayingType)
	}
	if sessions[1].NowPlayingTitle != "" {
		t.Error("sessions[1] has no NowPlayingItem, title should be empty")
	}
}

func TestGetJellyfinSessions_SkipsSystemSessions(t *testing.T) {
	// Sessions without UserName are system/device sessions — should be filtered.
	payload := `[{"DeviceName":"Server","Client":"System","LastActivityDate":"2026-04-06T12:00:00Z"}]`
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Sessions", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(payload))
		})
	})
	resetServices(t)

	sessions, err := GetJellyfinSessions(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("want 0 sessions (system sessions filtered), got %d", len(sessions))
	}
}

func TestHandleSessions_HappyPath(t *testing.T) {
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Sessions", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"UserName":"alice","DeviceName":"TV","Client":"Jellyfin","NowPlayingItem":{"Name":"Inception","Type":"Movie"}}]`))
		})
	})
	resetServices(t)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/sessions", nil)
	w := httptest.NewRecorder()
	handleSessions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var sessions []JellyfinSession
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(sessions) != 1 || sessions[0].NowPlayingTitle != "Inception" {
		t.Errorf("unexpected sessions: %+v", sessions)
	}
}

func TestHandleSessions_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/sessions", nil)
	w := httptest.NewRecorder()
	handleSessions(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
