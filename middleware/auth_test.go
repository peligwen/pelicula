package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeJellyfinAuthServer starts an httptest.Server with a /Users/AuthenticateByName
// handler. succeed controls whether it returns 200 (with isAdmin payload) or 401.
// The caller is responsible for closing the server.
func fakeJellyfinAuthServer(t *testing.T, succeed bool, isAdmin bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		if !succeed {
			http.Error(w, `{"Message":"Username or password is incorrect"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"User":{"Id":"jf-user-001","Name":"alice","Policy":{"IsAdministrator":` +
			boolStr(isAdmin) + `}},"AccessToken":"test-jf-token","ServerId":"srv1"}`))
	})
	srv := httptest.NewServer(mux)
	orig := jellyfinURL
	jellyfinURL = srv.URL
	t.Cleanup(func() {
		srv.Close()
		jellyfinURL = orig
	})
	return srv
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// newTestAuth creates an Auth for testing without touching the filesystem.
// The cleanup goroutine is harmless in tests (sleeps 10 min, then GC'd).
func newTestAuth(mode string) *Auth {
	return &Auth{
		mode:     mode,
		sessions: make(map[string]session),
		failures: make(map[string]*loginAttempts),
	}
}

// newTestJellyfinAuth creates an Auth in "jellyfin" mode for testing.
// store may be nil — a fresh RolesStore backed by a test DB is used.
// httpClient should point at an httptest.Server serving the Jellyfin API.
func newTestJellyfinAuth(t *testing.T, store *RolesStore, httpClient *http.Client) *Auth {
	t.Helper()
	if store == nil {
		store = NewRolesStore(testDB(t))
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Auth{
		mode:       "jellyfin",
		sessions:   make(map[string]session),
		failures:   make(map[string]*loginAttempts),
		rolesStore: store,
		httpClient: httpClient,
	}
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

// ── HandleLogin ─────────────────────────────────────────────────────────

func TestLogin_AuthOff(t *testing.T) {
	a := newTestAuth("off")
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
	a := newTestAuth("jellyfin")
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/login", nil)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestLogin_BadJSON(t *testing.T) {
	a := newTestAuth("jellyfin")
	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLogin_RateLimited(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, true, true)
	a := newTestJellyfinAuth(t, nil, srv.Client())
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
	a := newTestAuth("jellyfin")
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
	a := newTestAuth("jellyfin")
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/logout", nil)
	w := httptest.NewRecorder()
	a.HandleLogout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent)", w.Code)
	}
}

func TestLogout_MethodNotAllowed(t *testing.T) {
	a := newTestAuth("jellyfin")
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/logout", nil)
	w := httptest.NewRecorder()
	a.HandleLogout(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// ── HandleCheck ─────────────────────────────────────────────────────────

func TestCheck_AuthOff(t *testing.T) {
	a := newTestAuth("off")
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
	a := newTestAuth("jellyfin")
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
	a := newTestAuth("jellyfin")
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
	a := newTestAuth("jellyfin")
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check?nginx=1", nil)
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)
	// With ?nginx=1, must return 401 so nginx triggers @login_redirect
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestCheck_NginxSubrequest_ValidSession(t *testing.T) {
	a := newTestAuth("jellyfin")
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
	a := newTestAuth("jellyfin")
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
	a := newTestAuth("off")
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
	a := newTestAuth("jellyfin")
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
	a := newTestAuth("jellyfin")
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name   string
		role   UserRole
		guard  func(http.Handler) http.Handler
		wantOK bool
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
	a := newTestAuth("off")
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

// ── Remote role capping (Peligrosa defense-in-depth) ────────────────────

func TestIsRemoteRequest(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"present", "true", true},
		{"absent", "", false},
		{"wrong value", "false", false},
		{"garbage", "yes", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				req.Header.Set("X-Pelicula-Remote", c.header)
			}
			if got := isRemoteRequest(req); got != c.want {
				t.Errorf("isRemoteRequest = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEffectiveRole_LocalAdminUnchanged(t *testing.T) {
	sess := session{username: "alice", role: RoleAdmin}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := effectiveRole(sess, req); got != RoleAdmin {
		t.Errorf("effectiveRole = %q, want admin (local request)", got)
	}
}

func TestEffectiveRole_RemoteAdminCapped(t *testing.T) {
	sess := session{username: "alice", role: RoleAdmin}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Pelicula-Remote", "true")
	if got := effectiveRole(sess, req); got != RoleViewer {
		t.Errorf("effectiveRole = %q, want viewer (remote admin capped)", got)
	}
}

func TestEffectiveRole_RemoteManagerCapped(t *testing.T) {
	sess := session{username: "bob", role: RoleManager}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Pelicula-Remote", "true")
	if got := effectiveRole(sess, req); got != RoleViewer {
		t.Errorf("effectiveRole = %q, want viewer (remote manager capped)", got)
	}
}

func TestEffectiveRole_RemoteViewerUnchanged(t *testing.T) {
	sess := session{username: "carol", role: RoleViewer}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Pelicula-Remote", "true")
	if got := effectiveRole(sess, req); got != RoleViewer {
		t.Errorf("effectiveRole = %q, want viewer (already viewer)", got)
	}
}

func TestGuardRole_RemoteAdminBlockedByGuardManager(t *testing.T) {
	a := newTestAuth("jellyfin")
	token := insertSession(a, "alice", RoleAdmin, time.Now().Add(time.Hour))
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := a.GuardManager(dummy)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	addSessionCookie(req, token)
	req.Header.Set("X-Pelicula-Remote", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (remote admin capped below manager)", w.Code)
	}
}

func TestGuardRole_RemoteAdminPassesGuard(t *testing.T) {
	a := newTestAuth("jellyfin")
	token := insertSession(a, "alice", RoleAdmin, time.Now().Add(time.Hour))
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := a.Guard(dummy)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	addSessionCookie(req, token)
	req.Header.Set("X-Pelicula-Remote", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (remote admin can still access viewer endpoints)", w.Code)
	}
}

func TestHandleCheck_RemoteReturnsViewerRole(t *testing.T) {
	a := newTestAuth("jellyfin")
	token := insertSession(a, "alice", RoleAdmin, time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check", nil)
	addSessionCookie(req, token)
	req.Header.Set("X-Pelicula-Remote", "true")
	w := httptest.NewRecorder()
	a.HandleCheck(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["role"] != "viewer" {
		t.Errorf("role = %v, want viewer (remote admin capped)", m["role"])
	}
	if m["remote"] != true {
		t.Errorf("remote = %v, want true", m["remote"])
	}
}

func TestSessionFor_RemoteAdminReturnsViewer(t *testing.T) {
	a := newTestAuth("jellyfin")
	token := insertSession(a, "alice", RoleAdmin, time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	addSessionCookie(req, token)
	req.Header.Set("X-Pelicula-Remote", "true")
	username, role, ok := a.SessionFor(req)
	if !ok {
		t.Fatal("SessionFor returned false, want true")
	}
	if username != "alice" {
		t.Errorf("username = %q, want alice", username)
	}
	if role != RoleViewer {
		t.Errorf("role = %q, want viewer (remote admin capped)", role)
	}
}

// ── Session details ─────────────────────────────────────────────────────

func TestSession_TokenFormat(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, true, true)
	a := newTestJellyfinAuth(t, nil, srv.Client())
	body := strings.NewReader(`{"username":"alice","password":"pass"}`)
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
	srv := fakeJellyfinAuthServer(t, true, true)
	a := newTestJellyfinAuth(t, nil, srv.Client())
	body := strings.NewReader(`{"username":"alice","password":"pass"}`)
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

// ── HandleLogin: jellyfin mode ────────────────────────────────────────────────

func TestLogin_JellyfinMode_ValidCreds_DefaultsToViewer(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, true, false)
	a := newTestJellyfinAuth(t, nil, srv.Client())

	body := strings.NewReader(`{"username":"alice","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	m := parseJSONBody(t, w)
	if m["role"] != "viewer" {
		t.Errorf("role = %v, want viewer (default for new non-admin user)", m["role"])
	}
	// Confirm role was persisted in store
	role, ok := a.rolesStore.Lookup("jf-user-001")
	if !ok {
		t.Error("expected user to be persisted in roles store")
	} else if role != RoleViewer {
		t.Errorf("stored role = %q, want viewer", role)
	}
}

func TestLogin_JellyfinMode_JellyfinAdmin_GetsAdminRole(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, true, true)
	a := newTestJellyfinAuth(t, nil, srv.Client())

	body := strings.NewReader(`{"username":"alice","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["role"] != "admin" {
		t.Errorf("role = %v, want admin (Jellyfin admin always gets admin)", m["role"])
	}
}

func TestLogin_JellyfinMode_StoredRolePreserved(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, true, false)
	store := NewRolesStore(testDB(t))
	// Pre-seed a manager role for this user.
	_ = store.Upsert("jf-user-001", "alice", RoleManager)
	a := newTestJellyfinAuth(t, store, srv.Client())

	body := strings.NewReader(`{"username":"alice","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	m := parseJSONBody(t, w)
	if m["role"] != "manager" {
		t.Errorf("role = %v, want manager (stored role preserved)", m["role"])
	}
}

func TestLogin_JellyfinMode_InvalidCreds_Returns401(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, false, false)
	a := newTestJellyfinAuth(t, nil, srv.Client())

	body := strings.NewReader(`{"username":"alice","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestLogin_JellyfinMode_InvalidCreds_RecordsFailure(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, false, false)
	a := newTestJellyfinAuth(t, nil, srv.Client())
	ip := "10.0.0.1"

	for i := 0; i < 5; i++ {
		body := strings.NewReader(`{"username":"alice","password":"wrong"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
		req.Header.Set("X-Real-IP", ip)
		a.HandleLogin(httptest.NewRecorder(), req)
	}
	// 6th attempt should be rate-limited (no Jellyfin call)
	body := strings.NewReader(`{"username":"alice","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	req.Header.Set("X-Real-IP", ip)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after 5 failed attempts", w.Code)
	}
}

func TestLogin_JellyfinMode_JellyfinDown_Returns503(t *testing.T) {
	// Start a server then close it immediately to simulate unreachable Jellyfin.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	srv.Close() // force connection refused

	orig := jellyfinURL
	jellyfinURL = srv.URL
	t.Cleanup(func() { jellyfinURL = orig })

	a := newTestJellyfinAuth(t, nil, srv.Client())

	body := strings.NewReader(`{"username":"alice","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", body)
	w := httptest.NewRecorder()
	a.HandleLogin(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when Jellyfin is down", w.Code)
	}
}

// ── Session persistence round-trip ───────────────────────────────────────────

// TestSessionPersistence_RoundTrip verifies that a session created by one Auth
// instance is visible to a second Auth instance opened on the same DB, simulating
// a process restart where sessions must survive.
func TestSessionPersistence_RoundTrip(t *testing.T) {
	srv := fakeJellyfinAuthServer(t, true, true)
	db := testDB(t)

	// Auth1: perform a login — this writes the session to SQLite.
	a1 := NewAuth(AuthConfig{
		Mode:       "jellyfin",
		DB:         db,
		HTTPClient: srv.Client(),
	})

	loginBody := strings.NewReader(`{"username":"alice","password":"pass"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/api/pelicula/auth/login", loginBody)
	loginW := httptest.NewRecorder()
	a1.HandleLogin(loginW, loginReq)
	if loginW.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body: %s", loginW.Code, loginW.Body.String())
	}

	// Extract the session cookie from the login response.
	var sessionToken string
	for _, c := range loginW.Result().Cookies() {
		if c.Name == "pelicula_session" {
			sessionToken = c.Value
			break
		}
	}
	if sessionToken == "" {
		t.Fatal("no pelicula_session cookie in login response")
	}

	// Auth2: new instance on the same DB — must restore sessions from SQLite.
	a2 := NewAuth(AuthConfig{
		Mode:       "jellyfin",
		DB:         db,
		HTTPClient: srv.Client(),
	})

	// Verify the session is available via HandleCheck.
	checkReq := httptest.NewRequest(http.MethodGet, "/api/pelicula/auth/check", nil)
	checkReq.AddCookie(&http.Cookie{Name: "pelicula_session", Value: sessionToken})
	checkW := httptest.NewRecorder()
	a2.HandleCheck(checkW, checkReq)

	if checkW.Code != http.StatusOK {
		t.Fatalf("check status = %d, want 200 (session should be restored from DB)", checkW.Code)
	}
	m := parseJSONBody(t, checkW)
	if m["valid"] != true {
		t.Errorf("valid = %v, want true — session not restored from DB", m["valid"])
	}
	if m["username"] != "alice" {
		t.Errorf("username = %v, want alice", m["username"])
	}
}

// ── RolesStore ─────────────────────────────────────────────────────────────────

func TestRolesStore_RoundTrip(t *testing.T) {
	db := testDB(t)
	rs := NewRolesStore(db)

	if !rs.IsEmpty() {
		t.Error("fresh store should be empty")
	}

	if err := rs.Upsert("id1", "alice", RoleViewer); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := rs.Upsert("id2", "bob", RoleManager); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Second store on same DB — same data should be visible.
	rs2 := NewRolesStore(db)
	role, ok := rs2.Lookup("id1")
	if !ok || role != RoleViewer {
		t.Errorf("id1: got (%q, %v), want (viewer, true)", role, ok)
	}
	role, ok = rs2.Lookup("id2")
	if !ok || role != RoleManager {
		t.Errorf("id2: got (%q, %v), want (manager, true)", role, ok)
	}

	// Upsert update
	if err := rs2.Upsert("id1", "alice", RoleAdmin); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	role, _ = rs2.Lookup("id1")
	if role != RoleAdmin {
		t.Errorf("after update: id1 role = %q, want admin", role)
	}
	// Entry count must not grow on update
	if len(rs2.All()) != 2 {
		t.Errorf("expected 2 entries after upsert-update, got %d", len(rs2.All()))
	}

	// Unknown ID
	if _, ok := rs2.Lookup("unknown"); ok {
		t.Error("Lookup of unknown ID should return false")
	}
}
