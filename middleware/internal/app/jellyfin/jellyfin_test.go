package jellyfin_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pelicula-api/httputil"
	jfapp "pelicula-api/internal/app/jellyfin"
	jfclient "pelicula-api/internal/clients/jellyfin"
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
		w.Write([]byte(`{"AccessToken":"test-token"}`)) //nolint:errcheck
	})
}

// newFakeJellyfin starts an httptest.Server, applies fakeJellyfinAuth, then
// runs the provided setup func to add test-specific handlers.
// Returns a Handler wired to the fake server.
func newFakeJellyfin(t *testing.T, setup func(mux *http.ServeMux)) (*httptest.Server, *jfapp.Handler) {
	t.Helper()
	mux := http.NewServeMux()
	fakeJellyfinAuth(mux)
	if setup != nil {
		setup(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := jfclient.NewWithHTTPClient(srv.URL, srv.Client())
	h := jfapp.NewHandler(client, func() (string, error) { return "test-token", nil }, jfapp.ServiceUser)
	return srv, h
}

// ── GET /api/pelicula/users ──────────────────────────────────────────────────

func TestHandleUsers_GetHappyPath(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Name":"alice","Id":"id1","HasPassword":true,"LastLoginDate":"2026-01-01T00:00:00Z"}]`)) //nolint:errcheck
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/users", nil)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var users []jfapp.User
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
	t.Parallel()
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/users", nil)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

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
	t.Parallel()
	const testUserID = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + testUserID + `"}`)) //nolint:errcheck
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			// Handles /Users/{id}/Password and /Users/{id} DELETE
			w.WriteHeader(http.StatusNoContent)
		})
	})

	body := strings.NewReader(`{"username":"bob","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body)
	}
}

func TestHandleUsers_PostEmptyPassword(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	body := strings.NewReader(`{"username":"bob","password":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsers_PostMissingUsername(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	body := strings.NewReader(`{"username":"","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsers_PostInvalidJSON(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsers_PostMethodNotAllowed(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		method := method
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(method, "/api/pelicula/users", nil)
			w := httptest.NewRecorder()
			h.HandleUsers(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", w.Code)
			}
		})
	}
}

func TestHandleUsers_PostOversizedBody(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	// 2 MB of JSON-ish garbage — well over the 1 MB cap.
	giant := strings.Repeat(`{"username":"x","password":"`, 80_000) + `"}`
	body := strings.NewReader(giant)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized body", w.Code)
	}
}

func TestHandleUsers_PostForeignOrigin(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	body := strings.NewReader(`{"username":"bob","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginSoft(http.HandlerFunc(h.HandleUsers)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleUsers_UsernameValidation(t *testing.T) {
	t.Parallel()
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
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, h := newFakeJellyfin(t, nil)

			payload, _ := json.Marshal(map[string]string{"username": c.username, "password": "hunter2"})
			req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", strings.NewReader(string(payload)))
			w := httptest.NewRecorder()
			h.HandleUsers(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("username %q: status = %d, want 400", c.username, w.Code)
			}
		})
	}
}

// ── CreateUser rollback ───────────────────────────────────────────────────────

func TestCreateUser_PasswordSetFailsRollbackSucceeds(t *testing.T) {
	t.Parallel()
	var deleteCalls atomic.Int32
	const rollbackID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + rollbackID + `"}`)) //nolint:errcheck
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

	_, err := h.CreateUser("alice", "secret")
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

func TestCreateUser_PasswordSetFailsRollbackAlsoFails(t *testing.T) {
	t.Parallel()
	const badID = "aaaaaaaa-bbbb-cccc-dddd-ffffffffffff"
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + badID + `"}`)) //nolint:errcheck
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			// Both /Password POST and DELETE fail.
			http.Error(w, "server error", http.StatusInternalServerError)
		})
	})

	_, err := h.CreateUser("alice", "secret")
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

func TestCreateUser_NoIdInResponse(t *testing.T) {
	t.Parallel()
	var passwordCalls atomic.Int32

	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			// Missing Id field.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`)) //nolint:errcheck
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			passwordCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
	})

	_, err := h.CreateUser("alice", "secret")
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

func TestCreateUser_JellyfinCreateFailure(t *testing.T) {
	t.Parallel()
	var deleteCalls atomic.Int32

	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "conflict", http.StatusBadRequest)
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			deleteCalls.Add(1)
		})
	})

	_, err := h.CreateUser("alice", "secret")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if deleteCalls.Load() != 0 {
		t.Errorf("DELETE called %d times when create itself failed, want 0", deleteCalls.Load())
	}
}

func TestCreateUser_JellyfinBadRequestMapsTo400(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "name taken", http.StatusBadRequest)
		})
	})

	body := strings.NewReader(`{"username":"existing","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", body)
	w := httptest.NewRecorder()
	h.HandleUsers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when Jellyfin rejects name", w.Code)
	}
}

func TestCreateUser_NonUUIDIdRejected(t *testing.T) {
	t.Parallel()
	var passwordCalls atomic.Int32

	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Non-UUID id — should be rejected before any /Password call.
			w.Write([]byte(`{"Id":"../System/Shutdown"}`)) //nolint:errcheck
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			passwordCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
	})

	_, err := h.CreateUser("alice", "secret")
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

// ── ValidUsername ─────────────────────────────────────────────────────────────

func TestValidUsername(t *testing.T) {
	t.Parallel()
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
		if !jfapp.ValidUsername(v) {
			t.Errorf("ValidUsername(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if jfapp.ValidUsername(v) {
			t.Errorf("ValidUsername(%q) = true, want false", v)
		}
	}
}

// ── ErrPasswordRequired sentinel ──────────────────────────────────────────────

func TestCreateUser_EmptyPasswordReturnsSentinel(t *testing.T) {
	t.Parallel()
	// No fake server needed — the check fires before any HTTP call.
	_, h := newFakeJellyfin(t, nil)

	_, err := h.CreateUser("alice", "")
	if !errors.Is(err, jfapp.ErrPasswordRequired) {
		t.Errorf("expected ErrPasswordRequired, got %v", err)
	}
}

// ── ValidJellyfinID ───────────────────────────────────────────────────────────

func TestValidJellyfinID(t *testing.T) {
	t.Parallel()
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
		if !jfapp.ValidJellyfinID(v) {
			t.Errorf("ValidJellyfinID(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if jfapp.ValidJellyfinID(v) {
			t.Errorf("ValidJellyfinID(%q) = true, want false", v)
		}
	}
}

// ── User field mapping ────────────────────────────────────────────────────────

func TestListUsers_FieldMapping(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Format(time.RFC3339)
	payload := `[{"Name":"carol","Id":"cid","HasPassword":false,"LastLoginDate":"` + now + `","Policy":{"IsAdministrator":true}}]`

	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(payload)) //nolint:errcheck
		})
	})

	users, err := h.ListUsers()
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

// ── DeleteUser ────────────────────────────────────────────────────────────────

func TestDeleteUser_HappyPath(t *testing.T) {
	t.Parallel()
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	var deleteCalls atomic.Int32
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				deleteCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		})
	})

	if err := h.DeleteUser(uid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleteCalls.Load() != 1 {
		t.Errorf("DELETE called %d times, want 1", deleteCalls.Load())
	}
}

func TestDeleteUser_InvalidID(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)
	err := h.DeleteUser("../etc/passwd")
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
		w.Write([]byte(`[` + //nolint:errcheck
			`{"Id":"` + adminID + `","Name":"admin","HasPassword":true,"Policy":{"IsAdministrator":true}},` +
			`{"Id":"` + userID + `","Name":"bob","HasPassword":true,"Policy":{"IsAdministrator":false}}` +
			`]`))
	})
}

func TestHandleUserDelete_HappyPath(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		setupUsersHandler(mux, testAdminID, testUserID2)
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		})
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/"+testUserID2, nil)
	w := httptest.NewRecorder()
	h.HandleUsersWithID(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestHandleUserDelete_LastAdminProtection(t *testing.T) {
	t.Parallel()
	// Only one admin — deleting them should be refused.
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Id":"` + testAdminID + `","Name":"admin","HasPassword":true,"Policy":{"IsAdministrator":true}}]`)) //nolint:errcheck
		})
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/"+testAdminID, nil)
	w := httptest.NewRecorder()
	h.HandleUsersWithID(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (last admin protection)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "only admin") {
		t.Errorf("body = %q, want mention of 'only admin'", w.Body)
	}
}

func TestHandleUserDelete_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		setupUsersHandler(mux, testAdminID, testUserID2)
	})

	unknownID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/"+unknownID, nil)
	w := httptest.NewRecorder()
	h.HandleUsersWithID(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// ── SetUserPassword / handleUserPassword ──────────────────────────────────────

func TestSetUserPassword_HappyPath(t *testing.T) {
	t.Parallel()
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	var pwCalls atomic.Int32
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Password") {
				pwCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		})
	})

	if err := h.SetUserPassword(uid, "newpassword"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Two POSTs: step 1 clears the password (ResetPassword:true), step 2 sets the new one.
	if pwCalls.Load() != 2 {
		t.Errorf("/Password POST called %d times, want 2", pwCalls.Load())
	}
}

func TestSetUserPassword_EmptyPassword(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)
	err := h.SetUserPassword("3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21", "")
	if !errors.Is(err, jfapp.ErrPasswordRequired) {
		t.Errorf("expected ErrPasswordRequired, got %v", err)
	}
}

func TestSetUserPassword_InvalidID(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)
	err := h.SetUserPassword("not-a-uuid", "pass")
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestHandleUserPassword_HappyPath(t *testing.T) {
	t.Parallel()
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	body := strings.NewReader(`{"password":"newpass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users/"+uid+"/password", body)
	w := httptest.NewRecorder()
	h.HandleUsersWithID(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestHandleUserPassword_EmptyPassword(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	body := strings.NewReader(`{"password":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users/3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21/password", body)
	w := httptest.NewRecorder()
	h.HandleUsersWithID(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ── SetUserDisabled / handleUsersWithID /disable /enable ──────────────────────

func TestSetUserDisabled(t *testing.T) {
	t.Parallel()
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	var postedBody []byte
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/"+uid, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + uid + `","Name":"alice","Policy":{"IsAdministrator":false,"IsDisabled":false}}`)) //nolint:errcheck
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

	if err := h.SetUserDisabled(uid, true); err != nil {
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

// ── SetUserLibraryAccess ──────────────────────────────────────────────────────

func TestSetUserLibraryAccess(t *testing.T) {
	t.Parallel()
	const uid = "3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21"
	const moviesID = "folder-movies-id-0000000000000000"
	const tvID = "folder-tv-id-000000000000000000"
	var postedBody []byte

	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/"+uid, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"` + uid + `","Name":"alice","Policy":{"IsAdministrator":false,"IsDisabled":false,"EnableAllFolders":true,"EnabledFolders":[]}}`)) //nolint:errcheck
		})
		mux.HandleFunc("/Library/VirtualFolders", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"Name":"Movies","ItemId":"` + moviesID + `"},{"Name":"TV Shows","ItemId":"` + tvID + `"}]`)) //nolint:errcheck
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

	// movies=true, tv=false → should restrict to Movies folder only
	if err := h.SetUserLibraryAccess(uid, true, false); err != nil {
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

// ── HandleUsersWithID routing ─────────────────────────────────────────────────

func TestHandleUsersWithID_ForeignOrigin(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginSoft(http.HandlerFunc(h.HandleUsersWithID)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleUsersWithID_InvalidID(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/users/not-a-uuid", nil)
	w := httptest.NewRecorder()
	h.HandleUsersWithID(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleUsersWithID_PasswordMethodNotAllowed(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/users/3a4d9e71-6a1b-4f2c-9d12-98b4c76e3f21/password", nil)
	w := httptest.NewRecorder()
	h.HandleUsersWithID(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ── GetSessions / HandleSessions ─────────────────────────────────────────────

func TestGetSessions_HappyPath(t *testing.T) {
	t.Parallel()
	payload := `[
		{"UserName":"alice","DeviceName":"iPhone","Client":"Infuse","LastActivityDate":"2026-04-06T12:00:00Z",
		 "NowPlayingItem":{"Name":"Dune: Part Two","Type":"Movie"}},
		{"UserName":"bob","DeviceName":"TV","Client":"Jellyfin for Android TV","LastActivityDate":"2026-04-06T11:00:00Z"}
	]`
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Sessions", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(payload)) //nolint:errcheck
		})
	})

	sessions, err := h.GetSessions()
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

func TestGetSessions_SkipsSystemSessions(t *testing.T) {
	t.Parallel()
	// Sessions without UserName are system/device sessions — should be filtered.
	payload := `[{"DeviceName":"Server","Client":"System","LastActivityDate":"2026-04-06T12:00:00Z"}]`
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Sessions", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(payload)) //nolint:errcheck
		})
	})

	sessions, err := h.GetSessions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("want 0 sessions (system sessions filtered), got %d", len(sessions))
	}
}

func TestHandleSessions_HappyPath(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Sessions", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"UserName":"alice","DeviceName":"TV","Client":"Jellyfin","NowPlayingItem":{"Name":"Inception","Type":"Movie"}}]`)) //nolint:errcheck
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/sessions", nil)
	w := httptest.NewRecorder()
	h.HandleSessions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var sessions []jfapp.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(sessions) != 1 || sessions[0].NowPlayingTitle != "Inception" {
		t.Errorf("unexpected sessions: %+v", sessions)
	}
}

func TestHandleSessions_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	_, h := newFakeJellyfin(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/sessions", nil)
	w := httptest.NewRecorder()
	h.HandleSessions(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
