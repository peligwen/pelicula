package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	// authMiddleware in off mode by default — tests that need a specific mode
	// assign authMiddleware themselves.
	origAuth := authMiddleware
	authMiddleware = newTestAuth("users", "", nil) // non-off mode so POST isn't blocked
	t.Cleanup(func() { authMiddleware = origAuth })
	return srv
}

// resetServices points the global services at a dummy config dir so calls don't
// panic trying to open real files. jellyfinURL is already overridden by newFakeJellyfin.
func resetServices(t *testing.T) {
	t.Helper()
	orig := services
	services = NewServiceClients(t.TempDir())
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
	handleUsers(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleUsers_OffMode(t *testing.T) {
	// Restore global after test.
	orig := authMiddleware
	authMiddleware = newTestAuth("off", "", nil)
	t.Cleanup(func() { authMiddleware = orig })

	body := strings.NewReader(`{"username":"bob","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 when PELICULA_AUTH=off", w.Code)
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

	err := CreateJellyfinUser(services, "alice", "secret")
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

	err := CreateJellyfinUser(services, "alice", "secret")
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

	err := CreateJellyfinUser(services, "alice", "secret")
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

	err := CreateJellyfinUser(services, "alice", "secret")
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

	err := CreateJellyfinUser(services, "alice", "secret")
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

	err := CreateJellyfinUser(services, "alice", "")
	if !isErrPasswordRequired(err) {
		t.Errorf("expected ErrPasswordRequired, got %v", err)
	}
}

// isErrPasswordRequired is a test helper that mirrors the errors.Is check in handleUsers.
func isErrPasswordRequired(err error) bool {
	return err != nil && err.Error() == ErrPasswordRequired.Error()
}

// ── Session / auth wiring sanity ──────────────────────────────────────────────

// TestHandleUsers_OffMode_GetIsAllowed ensures the off-mode guard only blocks
// state-mutating POST, not the read-only GET (dashboard listing still works).
func TestHandleUsers_OffMode_GetIsAllowed(t *testing.T) {
	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		})
	})
	resetServices(t)

	// Switch authMiddleware to off mode.
	authMiddleware = newTestAuth("off", "", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/users", nil)
	w := httptest.NewRecorder()
	handleUsers(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET in off mode: status = %d, want 200", w.Code)
	}
}

// TestHandleUsers_NilAuthMiddlewareDoesNotPanic ensures that if authMiddleware
// is nil (e.g. in setup mode) the handler falls through without panicking.
func TestHandleUsers_NilAuthMiddlewareDoesNotPanic(t *testing.T) {
	orig := authMiddleware
	authMiddleware = nil
	t.Cleanup(func() { authMiddleware = orig })

	// Should not panic — nil guard is in the off-mode check.
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
	}
	invalid := []string{
		"",
		"not-a-uuid",
		"../System/Shutdown",
		"3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f2",  // too short
		"3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f210", // too long
		"3a4d9e71x6a1b-4f2c-9d12-98b4c76e3f21",  // wrong separator
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
	payload := `[{"Name":"carol","Id":"cid","HasPassword":false,"LastLoginDate":"` + now + `"}]`

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
}
