package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── handleOpenRegCheck ──────────────────────────────────────────────────────

func TestOpenRegCheck_Enabled(t *testing.T) {
	orig := openRegistration
	openRegistration = true
	t.Cleanup(func() { openRegistration = orig })

	a := newTestAuth("jellyfin")
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/register/check", nil)
	w := httptest.NewRecorder()
	a.HandleOpenRegCheck(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["open_registration"] != true {
		t.Errorf("open_registration = %v, want true", m["open_registration"])
	}
}

func TestOpenRegCheck_Disabled(t *testing.T) {
	orig := openRegistration
	openRegistration = false
	t.Cleanup(func() { openRegistration = orig })

	a := newTestAuth("jellyfin")
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/register/check", nil)
	w := httptest.NewRecorder()
	a.HandleOpenRegCheck(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["open_registration"] != false {
		t.Errorf("open_registration = %v, want false", m["open_registration"])
	}
}

func TestOpenRegCheck_MethodNotAllowed(t *testing.T) {
	a := newTestAuth("jellyfin")
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register/check", nil)
	w := httptest.NewRecorder()
	a.HandleOpenRegCheck(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// ── handleOpenRegister ──────────────────────────────────────────────────────

func setOpenRegistration(t *testing.T, val bool) {
	t.Helper()
	orig := openRegistration
	openRegistration = val
	t.Cleanup(func() { openRegistration = orig })
}

func TestOpenRegister_Disabled_Returns403(t *testing.T) {
	setOpenRegistration(t, false)
	body := strings.NewReader(`{"username":"alice","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestOpenRegister_AuthOff_Returns403(t *testing.T) {
	setOpenRegistration(t, true)
	origAuth := authMiddleware
	authMiddleware = newTestAuth("off")
	t.Cleanup(func() { authMiddleware = origAuth })

	body := strings.NewReader(`{"username":"alice","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (auth off)", w.Code)
	}
}

func TestOpenRegister_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/register", nil)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestOpenRegister_EmptyUsername_Returns400(t *testing.T) {
	setOpenRegistration(t, true)
	origAuth := authMiddleware
	authMiddleware = newTestAuth("jellyfin")
	t.Cleanup(func() { authMiddleware = origAuth })

	body := strings.NewReader(`{"username":"","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestOpenRegister_EmptyPassword_Returns400(t *testing.T) {
	setOpenRegistration(t, true)
	origAuth := authMiddleware
	authMiddleware = newTestAuth("jellyfin")
	t.Cleanup(func() { authMiddleware = origAuth })

	body := strings.NewReader(`{"username":"alice","password":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestOpenRegister_Success(t *testing.T) {
	setOpenRegistration(t, true)

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4","Name":"alice"}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	body := strings.NewReader(`{"username":"alice","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	m := parseJSONBody(t, w)
	if m["status"] != "ok" {
		t.Errorf("status = %v, want ok", m["status"])
	}
}

func TestOpenRegister_AssignsViewerRole(t *testing.T) {
	setOpenRegistration(t, true)

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5","Name":"bob"}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	// Set authMiddleware AFTER newFakeJellyfin (which also sets it) so the
	// rolesStore we inspect is the one the handler actually writes to.
	// Use the global services (set by resetServices above) so CreateUser works.
	store := NewRolesStore(testDB(t))
	// Seed an existing admin so IsEmpty() returns false — this test verifies
	// that subsequent registrants get viewer, not admin.
	_ = store.Upsert("a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", "admin", RoleAdmin)
	auth := &Auth{
		mode:       "jellyfin",
		sessions:   make(map[string]session),
		failures:   make(map[string]*loginAttempts),
		rolesStore: store,
		jellyfin:   NewJellyfinHTTPClient(http.DefaultClient, services),
	}
	origAuth := authMiddleware
	authMiddleware = auth
	t.Cleanup(func() { authMiddleware = origAuth })

	body := strings.NewReader(`{"username":"bob","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	role, ok := store.Lookup("b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5")
	if !ok {
		t.Fatal("expected user to be in roles store")
	}
	if role != RoleViewer {
		t.Errorf("role = %q, want viewer", role)
	}
}

func TestOpenRegister_InitialSetupAssignsAdmin(t *testing.T) {
	// Open registration is OFF, but initial_setup (empty roles store) should
	// still allow registration and assign admin role.
	setOpenRegistration(t, false)

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"Id":"c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6","Name":"gwen"}`))
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	resetServices(t)

	// Fresh roles store — IsEmpty() returns true.
	// Use the global services (set by resetServices above) so CreateUser works.
	store := NewRolesStore(testDB(t))
	auth := &Auth{
		mode:       "jellyfin",
		sessions:   make(map[string]session),
		failures:   make(map[string]*loginAttempts),
		rolesStore: store,
		jellyfin:   NewJellyfinHTTPClient(http.DefaultClient, services),
	}
	origAuth := authMiddleware
	authMiddleware = auth
	t.Cleanup(func() { authMiddleware = origAuth })

	body := strings.NewReader(`{"username":"gwen","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	role, ok := store.Lookup("c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6")
	if !ok {
		t.Fatal("expected user to be in roles store")
	}
	if role != RoleAdmin {
		t.Errorf("role = %q, want admin", role)
	}
}

func TestOpenRegister_UsernameTaken_Returns409(t *testing.T) {
	setOpenRegistration(t, true)

	newFakeJellyfin(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"Message":"A user with that name already exists."}`, http.StatusBadRequest)
		})
	})
	resetServices(t)

	body := strings.NewReader(`{"username":"alice","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["code"] != "username_taken" {
		t.Errorf("code = %v, want username_taken", m["code"])
	}
}

func TestOpenRegister_RateLimited_Returns429(t *testing.T) {
	setOpenRegistration(t, true)
	auth := newTestAuth("jellyfin")
	origAuth := authMiddleware
	authMiddleware = auth
	t.Cleanup(func() { authMiddleware = origAuth })

	ip := "10.0.0.99"
	for i := 0; i < 5; i++ {
		auth.recordFailure(ip)
	}

	body := strings.NewReader(`{"username":"alice","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", body)
	req.Header.Set("X-Real-IP", ip)
	w := httptest.NewRecorder()
	authMiddleware.HandleOpenRegister(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
}
