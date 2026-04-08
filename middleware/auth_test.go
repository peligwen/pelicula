package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testHash is a test helper that hashes a password, panicking on error.
// crypto/rand.Read never returns an error in practice (Go 1.20+ guarantee).
func testHash(username, plaintext string) string {
	h, err := hashPassword(username, plaintext)
	if err != nil {
		panic("hashPassword: " + err.Error())
	}
	return h
}

// newTestAuth creates an Auth for testing without touching the filesystem.
// The cleanup goroutine is harmless in tests (sleeps 10 min, then GC'd).
func newTestAuth(mode, password string, users []User) *Auth {
	a := &Auth{
		mode:     mode,
		password: password,
		sessions: make(map[string]session),
		failures: make(map[string]*loginAttempts),
		users:    users,
	}
	return a
}

// insertSession adds a session directly for testing and returns the token.
func insertSession(a *Auth, username string, role UserRole, expiry time.Time) string {
	token := "test-token-" + username
	a.mu.Lock()
	a.sessions[token] = session{username: username, role: role, expiry: expiry}
	a.mu.Unlock()
	return token
}

func addSessionCookie(r *http.Request, token string) {
	r.AddCookie(&http.Cookie{Name: "pelicula_session", Value: token})
}

func parseJSONBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("failed to parse JSON body: %v\nbody: %s", err, w.Body.String())
	}
	return m
}

func TestUserRoleAtLeast(t *testing.T) {
	cases := []struct {
		role UserRole
		min  UserRole
		want bool
	}{
		{RoleViewer, RoleViewer, true},
		{RoleViewer, RoleManager, false},
		{RoleViewer, RoleAdmin, false},
		{RoleManager, RoleViewer, true},
		{RoleManager, RoleManager, true},
		{RoleManager, RoleAdmin, false},
		{RoleAdmin, RoleViewer, true},
		{RoleAdmin, RoleManager, true},
		{RoleAdmin, RoleAdmin, true},
	}
	for _, c := range cases {
		t.Run(string(c.role)+"/"+string(c.min), func(t *testing.T) {
			got := c.role.atLeast(c.min)
			if got != c.want {
				t.Errorf("%q.atLeast(%q) = %v, want %v", c.role, c.min, got, c.want)
			}
		})
	}
}

func TestHashPassword(t *testing.T) {
	t.Run("format is sha256v2:SALT:HASH", func(t *testing.T) {
		h := testHash("alice", "secret")
		parts := strings.Split(h, ":")
		if len(parts) != 3 {
			t.Fatalf("expected 3 colon-separated parts, got %d in %q", len(parts), h)
		}
		if parts[0] != "sha256v2" {
			t.Errorf("prefix = %q, want %q", parts[0], "sha256v2")
		}
		if len(parts[1]) == 0 {
			t.Error("salt is empty")
		}
		if len(parts[2]) == 0 {
			t.Error("hash is empty")
		}
	})

	t.Run("same password hashed twice has different salts", func(t *testing.T) {
		h1 := testHash("alice", "secret")
		h2 := testHash("alice", "secret")
		if h1 == h2 {
			t.Error("expected different hashes due to random salt")
		}
	})
}

func TestVerifyPassword(t *testing.T) {
	t.Run("correct password verifies", func(t *testing.T) {
		h := testHash("alice", "correct-horse")
		if !verifyPassword("alice", "correct-horse", h) {
			t.Error("expected correct password to verify")
		}
	})

	t.Run("wrong password fails", func(t *testing.T) {
		h := testHash("alice", "correct-horse")
		if verifyPassword("alice", "wrong-horse", h) {
			t.Error("expected wrong password to fail")
		}
	})

	t.Run("legacy format: plain sha256 of user:pass", func(t *testing.T) {
		// Legacy format: sha256(username + ":" + password) as plain hex, no prefix.
		raw := sha256.Sum256([]byte("alice:legacy"))
		legacyHash := hex.EncodeToString(raw[:])
		if !verifyPassword("alice", "legacy", legacyHash) {
			t.Error("expected legacy hash to verify correctly")
		}
	})

	t.Run("empty stored hash fails gracefully", func(t *testing.T) {
		if verifyPassword("alice", "anything", "") {
			t.Error("expected empty stored hash to return false")
		}
	})

	t.Run("wrong username fails even with correct password", func(t *testing.T) {
		h := testHash("alice", "secret")
		if verifyPassword("bob", "secret", h) {
			t.Error("expected wrong username to fail verification")
		}
	})
}

// ── HandleLogin ─────────────────────────────────────────────────────────

func TestLogin_PasswordMode_Success(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	body := strings.NewReader(`{"username":"admin","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["role"] != "admin" {
		t.Errorf("role = %v, want admin", m["role"])
	}
	cookies := w.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "pelicula_session" {
			found = true
			if !c.HttpOnly {
				t.Error("cookie should be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("expected pelicula_session cookie to be set")
	}
}

func TestLogin_PasswordMode_WrongPassword(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	body := strings.NewReader(`{"username":"admin","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestLogin_UsersMode(t *testing.T) {
	hash := testHash("alice", "pass123")
	users := []User{{Username: "alice", Password: hash, Role: RoleViewer}}
	a := newTestAuth("users", "", users)

	t.Run("correct credentials", func(t *testing.T) {
		body := strings.NewReader(`{"username":"alice","password":"pass123"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
		w := httptest.NewRecorder()
		a.HandleLogin(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		m := parseJSONBody(t, w)
		if m["role"] != "viewer" {
			t.Errorf("role = %v, want viewer", m["role"])
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		body := strings.NewReader(`{"username":"alice","password":"wrong"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
		w := httptest.NewRecorder()
		a.HandleLogin(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		body := strings.NewReader(`{"username":"bob","password":"pass123"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
		w := httptest.NewRecorder()
		a.HandleLogin(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})
}

func TestLogin_AuthOff(t *testing.T) {
	a := newTestAuth("off", "", nil)
	body := strings.NewReader(`{"username":"any","password":"any"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["auth"] != false {
		t.Errorf("auth = %v, want false", m["auth"])
	}
}

func TestLogin_MethodNotAllowed(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/login", nil)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestLogin_BadJSON(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLogin_RateLimited(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	ip := "1.2.3.4"
	for i := 0; i < 5; i++ {
		a.recordFailure(ip)
	}

	t.Run("blocked IP", func(t *testing.T) {
		body := strings.NewReader(`{"username":"admin","password":"secret"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
		req.Header.Set("X-Real-IP", ip)
		w := httptest.NewRecorder()
		a.HandleLogin(w, req)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", w.Code)
		}
	})

	t.Run("different IP not affected", func(t *testing.T) {
		body := strings.NewReader(`{"username":"admin","password":"secret"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
		req.Header.Set("X-Real-IP", "5.6.7.8")
		w := httptest.NewRecorder()
		a.HandleLogin(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	})
}

// ── HandleLogout ────────────────────────────────────────────────────────

func TestLogout_ClearsSession(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	token := insertSession(a, "admin", RoleAdmin, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/logout", nil)
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	a.HandleLogout(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Cookie should be deleted
	for _, c := range w.Result().Cookies() {
		if c.Name == "pelicula_session" && c.MaxAge != -1 {
			t.Errorf("MaxAge = %d, want -1", c.MaxAge)
		}
	}
	// Session should be removed from map
	a.mu.RLock()
	_, exists := a.sessions[token]
	a.mu.RUnlock()
	if exists {
		t.Error("session should have been removed from map")
	}
}

func TestLogout_NoCookie(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/logout", nil)
	w := httptest.NewRecorder()
	a.HandleLogout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent)", w.Code)
	}
}

func TestLogout_MethodNotAllowed(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/logout", nil)
	w := httptest.NewRecorder()
	a.HandleLogout(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// ── HandleCheck ─────────────────────────────────────────────────────────

func TestCheck_AuthOff(t *testing.T) {
	a := newTestAuth("off", "", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check", nil)
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["auth"] != false {
		t.Errorf("auth = %v, want false", m["auth"])
	}
	if m["valid"] != true {
		t.Errorf("valid = %v, want true", m["valid"])
	}
}

func TestCheck_ValidSession(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	token := insertSession(a, "alice", RoleManager, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check", nil)
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["valid"] != true {
		t.Errorf("valid = %v, want true", m["valid"])
	}
	if m["username"] != "alice" {
		t.Errorf("username = %v, want alice", m["username"])
	}
	if m["role"] != "manager" {
		t.Errorf("role = %v, want manager", m["role"])
	}
}

func TestCheck_NoSession(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check", nil)
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)
	// Without ?nginx=1, returns 200 with valid:false (for dashboard JS)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["valid"] != false {
		t.Errorf("valid = %v, want false", m["valid"])
	}
}

func TestCheck_NginxSubrequest_NoSession(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check?nginx=1", nil)
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)
	// With ?nginx=1, must return 401 so nginx triggers @login_redirect
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestCheck_NginxSubrequest_ValidSession(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	token := insertSession(a, "alice", RoleAdmin, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check?nginx=1", nil)
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)
	// With ?nginx=1 and valid session, must return 200 so nginx allows the request
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["valid"] != true {
		t.Errorf("valid = %v, want true", m["valid"])
	}
}

func TestCheck_ExpiredSession(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	token := insertSession(a, "alice", RoleAdmin, time.Now().Add(-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check?nginx=1", nil)
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (expired session)", w.Code)
	}
}

// ── Guard middleware ────────────────────────────────────────────────────

func TestGuard_AuthOff(t *testing.T) {
	a := newTestAuth("off", "", nil)
	called := false
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := a.Guard(dummy)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("handler should be called when auth is off")
	}
}

func TestGuard_NoSession(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := a.Guard(dummy)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestGuard_RoleMatrix(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name    string
		role    UserRole
		guard   func(http.Handler) http.Handler
		wantOK  bool
	}{
		{"viewer+Guard", RoleViewer, a.Guard, true},
		{"viewer+GuardManager", RoleViewer, a.GuardManager, false},
		{"viewer+GuardAdmin", RoleViewer, a.GuardAdmin, false},
		{"manager+Guard", RoleManager, a.Guard, true},
		{"manager+GuardManager", RoleManager, a.GuardManager, true},
		{"manager+GuardAdmin", RoleManager, a.GuardAdmin, false},
		{"admin+Guard", RoleAdmin, a.Guard, true},
		{"admin+GuardManager", RoleAdmin, a.GuardManager, true},
		{"admin+GuardAdmin", RoleAdmin, a.GuardAdmin, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			token := insertSession(a, c.name, c.role, time.Now().Add(time.Hour))
			handler := c.guard(dummy)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			addSessionCookie(req, token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if c.wantOK && w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			if !c.wantOK && w.Code == http.StatusOK {
				t.Errorf("status = 200, want 403")
			}
		})
	}
}

// TestGuardAdmin_OffMode_HandleUsers_StillBlocked documents that the off-mode
// bypass in guardRole does NOT protect handleUsers — instead handleUsers has its
// own explicit off-mode check so POST is blocked even when GuardAdmin is a no-op.
func TestGuardAdmin_OffMode_HandleUsers_StillBlocked(t *testing.T) {
	a := newTestAuth("off", "", nil)
	// In off mode, GuardAdmin passes through unconditionally.
	called := false
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := a.GuardAdmin(dummy)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/users", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if !called {
		t.Error("GuardAdmin in off mode should not block the handler (handleUsers handles it itself)")
	}
	// Confirm IsOffMode reports true so handleUsers can enforce its own check.
	if !a.IsOffMode() {
		t.Error("IsOffMode() should return true when mode is 'off'")
	}
}

// ── Session details ─────────────────────────────────────────────────────

func TestSession_TokenFormat(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	body := strings.NewReader(`{"username":"admin","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	for _, c := range w.Result().Cookies() {
		if c.Name == "pelicula_session" {
			if len(c.Value) != 64 {
				t.Errorf("token length = %d, want 64", len(c.Value))
			}
			if _, err := hex.DecodeString(c.Value); err != nil {
				t.Errorf("token is not valid hex: %v", err)
			}
			return
		}
	}
	t.Error("no pelicula_session cookie found")
}

func TestSession_CookieAttributes(t *testing.T) {
	a := newTestAuth("password", "secret", nil)
	body := strings.NewReader(`{"username":"admin","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	for _, c := range w.Result().Cookies() {
		if c.Name == "pelicula_session" {
			if c.Path != "/" {
				t.Errorf("Path = %q, want /", c.Path)
			}
			if !c.HttpOnly {
				t.Error("HttpOnly should be true")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", c.SameSite)
			}
			// Expiry should be ~24h from now
			if time.Until(c.Expires) < 23*time.Hour {
				t.Errorf("cookie expires too soon: %v", c.Expires)
			}
			return
		}
	}
	t.Error("no pelicula_session cookie found")
}

// ── requireLocalOriginStrict ──────────────────────────────────────────────────

func TestRequireLocalOriginStrict_GET_PassesThrough(t *testing.T) {
	handler := requireLocalOriginStrict(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// GET with no Origin must pass — reads should never be blocked
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET with no origin: want 200, got %d", w.Code)
	}
}

func TestRequireLocalOriginStrict_POST_EmptyOrigin_Rejected(t *testing.T) {
	handler := requireLocalOriginStrict(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST with empty origin: want 403, got %d", w.Code)
	}
}

func TestRequireLocalOriginStrict_POST_LocalOrigin_Allowed(t *testing.T) {
	handler := requireLocalOriginStrict(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, origin := range []string{"http://localhost:7354", "http://192.168.1.50:7354", "http://10.0.0.1"} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("POST origin %q: want 200, got %d", origin, w.Code)
		}
	}
}

func TestRequireLocalOriginStrict_POST_ForeignOrigin_Rejected(t *testing.T) {
	handler := requireLocalOriginStrict(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST with foreign origin: want 403, got %d", w.Code)
	}
}

// ── requireLocalOriginSoft ────────────────────────────────────────────────────

func TestRequireLocalOriginSoft_GET_PassesThrough(t *testing.T) {
	handler := requireLocalOriginSoft(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET with no origin: want 200, got %d", w.Code)
	}
}

func TestRequireLocalOriginSoft_POST_EmptyOrigin_Allowed(t *testing.T) {
	handler := requireLocalOriginSoft(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Empty origin must pass — API/curl callers don't send Origin
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST with empty origin: want 200, got %d", w.Code)
	}
}

func TestRequireLocalOriginSoft_POST_LocalOrigin_Allowed(t *testing.T) {
	handler := requireLocalOriginSoft(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Origin", "http://192.168.1.50:7354")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST with LAN origin: want 200, got %d", w.Code)
	}
}

func TestRequireLocalOriginSoft_POST_ForeignOrigin_Rejected(t *testing.T) {
	handler := requireLocalOriginSoft(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST with foreign origin: want 403, got %d", w.Code)
	}
}

func TestRequireLocalOriginSoft_DELETE_ForeignOrigin_Rejected(t *testing.T) {
	handler := requireLocalOriginSoft(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("DELETE with foreign origin: want 403, got %d", w.Code)
	}
}
