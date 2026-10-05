package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pelicula/internal/store"
)

// fakeJF is one fake standing in for Identity, UserCreator and UserAdmin.
type fakeJF struct {
	mu       sync.Mutex
	users    map[string]fakeUser
	authErr  error // returned by AuthenticateByName when set (after the user check is skipped)
	tokenErr error
	createFn func(name string) error
	created  []string
}

type fakeUser struct {
	password string
	admin    bool
}

func newFakeJF() *fakeJF {
	return &fakeJF{users: map[string]fakeUser{
		"alice": {password: "alice-pass", admin: true},
		"bob":   {password: "bob-pass"},
		"mgr":   {password: "mgr-pass"},
	}}
}

func (f *fakeJF) AuthenticateByName(_ context.Context, username, password string) (*LoginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authErr != nil {
		return nil, f.authErr
	}
	u, ok := f.users[username]
	if !ok || u.password != password {
		return nil, fmt.Errorf("jellyfin: HTTP 401: %w", errors.New("bad login"))
	}
	return &LoginResult{Token: "jf-token-" + username, UserID: "id-" + username, Username: username, IsAdmin: u.admin}, nil
}

func (f *fakeJF) Token(context.Context) (string, error) {
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
	return "admin-token", nil
}

func (f *fakeJF) CreateUser(_ context.Context, token, name, password string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token != "admin-token" {
		return "", errors.New("jellyfin: HTTP 401")
	}
	if f.createFn != nil {
		if err := f.createFn(name); err != nil {
			return "", err
		}
	}
	if _, ok := f.users[name]; ok {
		return "", errors.New("jellyfin: HTTP 400: user already exists")
	}
	f.users[name] = fakeUser{password: password}
	f.created = append(f.created, name)
	return "id-" + name, nil
}

type env struct {
	t  *testing.T
	st *store.Store
	jf *fakeJF
	a  *Auth
	h  http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	jf := newFakeJF()
	a := New(Deps{Store: st, Jellyfin: jf, Admin: jf, Users: jf, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.Handle("GET /test/viewer", a.GuardFunc(store.RoleViewer, whoami))
	mux.Handle("GET /test/manager", a.GuardFunc(store.RoleManager, whoami))
	mux.Handle("GET /test/admin", a.Guard(store.RoleAdmin, http.HandlerFunc(whoami)))
	return &env{t: t, st: st, jf: jf, a: a, h: mux}
}

func whoami(w http.ResponseWriter, r *http.Request) {
	s := SessionFrom(r.Context())
	if s == nil {
		http.Error(w, "no session in context", 500)
		return
	}
	fmt.Fprint(w, s.Username)
}

func (e *env) do(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	return nil
}

// login logs in through the real handler and returns the session cookie.
func (e *env) login(user, pass string) *http.Cookie {
	e.t.Helper()
	rec := e.do("POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, pass))
	if rec.Code != 200 {
		e.t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body)
	}
	c := sessionCookie(rec)
	if c == nil {
		e.t.Fatal("no session cookie")
	}
	return c
}

// loginAs logs in a user with the given stored role.
func (e *env) loginAs(user string, role store.Role) *http.Cookie {
	e.t.Helper()
	if err := e.st.SetRole(context.Background(), user, role); err != nil {
		e.t.Fatal(err)
	}
	return e.login(user, e.jf.users[user].password)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b map[string]string
	decode(t, rec, &b)
	if b["error"] == "" {
		t.Fatalf("body is not an error object: %q", rec.Body)
	}
	return b["error"]
}

func TestLoginSuccessSetsCookieAndPersistsDefaultRole(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	rec := e.do("POST", "/api/auth/login", `{"username":"bob","password":"bob-pass"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct{ Username, Role string }
	decode(t, rec, &got)
	if got.Username != "bob" || got.Role != "viewer" {
		t.Fatalf("body = %+v", got)
	}

	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("no cookie")
	}
	if len(c.Value) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(c.Value))
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie attrs: %+v", c)
	}
	if want := int(30 * 24 * time.Hour / time.Second); c.MaxAge != want {
		t.Errorf("Max-Age = %d, want %d", c.MaxAge, want)
	}
	if c.Secure {
		t.Error("cookie must not be Secure over plain HTTP")
	}

	role, ok, err := e.st.GetRole(ctx, "bob")
	if err != nil || !ok || role != store.RoleViewer {
		t.Fatalf("persisted role = %q ok=%v err=%v", role, ok, err)
	}
	sess, err := e.st.GetSession(ctx, c.Value)
	if err != nil || sess == nil {
		t.Fatalf("session not stored: %v %v", sess, err)
	}
	if sess.Username != "bob" || sess.Role != store.RoleViewer || sess.JellyfinToken != "jf-token-bob" || sess.JellyfinUserID != "id-bob" {
		t.Errorf("session = %+v", sess)
	}

	// Jellyfin admin on first login becomes admin and is persisted.
	rec = e.do("POST", "/api/auth/login", `{"username":"alice","password":"alice-pass"}`)
	decode(t, rec, &got)
	if got.Role != "admin" {
		t.Errorf("alice role = %q, want admin", got.Role)
	}
	if role, _, _ := e.st.GetRole(ctx, "alice"); role != store.RoleAdmin {
		t.Errorf("alice persisted role = %q", role)
	}
}

func TestLoginStoredRoleWins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.st.SetRole(ctx, "bob", store.RoleManager); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetRole(ctx, "alice", store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]string{"bob": "manager", "alice": "viewer"} {
		rec := e.do("POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, e.jf.users[user].password))
		var got struct{ Role string }
		decode(t, rec, &got)
		if got.Role != want {
			t.Errorf("%s role = %q, want %q", user, got.Role, want)
		}
	}
}

func TestLoginCustomTTL(t *testing.T) {
	e := newEnv(t)
	e.a = New(Deps{Store: e.st, Jellyfin: e.jf, SessionTTL: time.Hour})
	mux := http.NewServeMux()
	e.a.Routes(mux)
	e.h = mux
	rec := e.do("POST", "/api/auth/login", `{"username":"bob","password":"bob-pass"}`)
	if c := sessionCookie(rec); c == nil || c.MaxAge != 3600 {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestLoginSecureCookieBehindHTTPSProxy(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"bob","password":"bob-pass"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if c := sessionCookie(rec); c == nil || !c.Secure {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestLoginBadPassword(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/api/auth/login", `{"username":"bob","password":"nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	errorBody(t, rec)
	if sessionCookie(rec) != nil {
		t.Error("cookie set on failed login")
	}
	if _, ok, _ := e.st.GetRole(context.Background(), "bob"); ok {
		t.Error("role persisted on failed login")
	}
}

func TestLoginErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"sentinel", ErrBadCredentials, 401},
		{"wrapped sentinel", fmt.Errorf("jellyfin: %w", ErrBadCredentials), 401},
		{"http 401 text", errors.New("jellyfin: POST /Users/AuthenticateByName: HTTP 401: nope"), 401},
		{"unauthorized text", errors.New("jellyfin: unauthorized"), 401},
		{"connection refused", errors.New("dial tcp 10.0.0.5:8096: connect: connection refused"), 503},
		{"timeout", context.DeadlineExceeded, 503},
		{"server error", errors.New("jellyfin: HTTP 500: boom"), 503},
		{"401 inside a port", errors.New("dial tcp 10.0.0.5:8401: connection refused"), 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.jf.authErr = tc.err
			rec := e.do("POST", "/api/auth/login", `{"username":"bob","password":"bob-pass"}`)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}
			errorBody(t, rec)
			if sessionCookie(rec) != nil {
				t.Error("cookie set on failed login")
			}
		})
	}
}

func TestLoginJellyfinDown(t *testing.T) {
	e := newEnv(t)
	e.jf.authErr = errors.New("connection refused")
	rec := e.do("POST", "/api/auth/login", `{"username":"bob","password":"bob-pass"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLoginBadRequests(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{``, `{}`, `not json`, `{"username":"bob"}`, `{"password":"x"}`} {
		rec := e.do("POST", "/api/auth/login", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d", body, rec.Code)
		}
		errorBody(t, rec)
	}
}

func TestMeAndCheck(t *testing.T) {
	e := newEnv(t)

	// No cookie.
	rec := e.do("GET", "/api/auth/me", "")
	if rec.Code != 401 {
		t.Fatalf("me without cookie = %d", rec.Code)
	}
	errorBody(t, rec)
	rec = e.do("GET", "/api/auth/check", "")
	if rec.Code != 401 || rec.Body.Len() != 0 {
		t.Fatalf("check without cookie = %d body %q", rec.Code, rec.Body)
	}

	// Garbage cookie.
	bogus := &http.Cookie{Name: CookieName, Value: "deadbeef"}
	if rec = e.do("GET", "/api/auth/me", "", bogus); rec.Code != 401 {
		t.Errorf("me with bogus cookie = %d", rec.Code)
	}
	if rec = e.do("GET", "/api/auth/check", "", bogus); rec.Code != 401 {
		t.Errorf("check with bogus cookie = %d", rec.Code)
	}

	// Valid session.
	c := e.login("bob", "bob-pass")
	rec = e.do("GET", "/api/auth/me", "", c)
	if rec.Code != 200 {
		t.Fatalf("me = %d", rec.Code)
	}
	var got struct{ Username, Role string }
	decode(t, rec, &got)
	if got.Username != "bob" || got.Role != "viewer" {
		t.Errorf("me = %+v", got)
	}
	rec = e.do("GET", "/api/auth/check", "", c)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Errorf("check = %d body %q", rec.Code, rec.Body)
	}

	// ?min raises the bar: a viewer is 403 against min=admin, 204 against
	// min=viewer, and a bad role name is 400.
	if rec = e.do("GET", "/api/auth/check?min=admin", "", c); rec.Code != 403 {
		t.Errorf("viewer check min=admin = %d, want 403", rec.Code)
	}
	if rec = e.do("GET", "/api/auth/check?min=viewer", "", c); rec.Code != 204 {
		t.Errorf("viewer check min=viewer = %d, want 204", rec.Code)
	}
	if rec = e.do("GET", "/api/auth/check?min=root", "", c); rec.Code != 400 {
		t.Errorf("check min=root = %d, want 400", rec.Code)
	}
	if rec = e.do("GET", "/api/auth/check?min=admin", ""); rec.Code != 401 {
		t.Errorf("anonymous check min=admin = %d, want 401", rec.Code)
	}
	if err := e.st.SetRole(context.Background(), "bob", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if rec = e.do("GET", "/api/auth/check?min=admin", "", c); rec.Code != 204 {
		t.Errorf("admin check min=admin = %d, want 204", rec.Code)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	e := newEnv(t)
	err := e.st.CreateSession(context.Background(), store.Session{
		Token: "old", Username: "bob", Role: store.RoleAdmin, ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Cookie{Name: CookieName, Value: "old"}
	for _, p := range []string{"/api/auth/me", "/api/auth/check", "/test/viewer"} {
		if rec := e.do("GET", p, "", c); rec.Code != 401 {
			t.Errorf("%s = %d, want 401", p, rec.Code)
		}
	}
}

func TestGuardRoleOrdering(t *testing.T) {
	e := newEnv(t)
	viewer := e.loginAs("bob", store.RoleViewer)
	manager := e.loginAs("mgr", store.RoleManager)
	admin := e.login("alice", "alice-pass")

	cases := []struct {
		path   string
		cookie *http.Cookie
		want   int
	}{
		{"/test/viewer", nil, 401},
		{"/test/manager", nil, 401},
		{"/test/viewer", viewer, 200},
		{"/test/manager", viewer, 403},
		{"/test/admin", viewer, 403},
		{"/test/manager", manager, 200},
		{"/test/admin", manager, 403},
		{"/test/viewer", admin, 200},
		{"/test/manager", admin, 200},
		{"/test/admin", admin, 200},
	}
	for _, tc := range cases {
		var rec *httptest.ResponseRecorder
		if tc.cookie == nil {
			rec = e.do("GET", tc.path, "")
		} else {
			rec = e.do("GET", tc.path, "", tc.cookie)
		}
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.path, rec.Code, tc.want)
			continue
		}
		switch tc.want {
		case 401:
			if got := errorBody(t, rec); got != "unauthorized" {
				t.Errorf("%s: error = %q", tc.path, got)
			}
		case 403:
			if got := errorBody(t, rec); got != "forbidden" {
				t.Errorf("%s: error = %q", tc.path, got)
			}
		}
	}

	rec := e.do("GET", "/test/admin", "", admin)
	if rec.Body.String() != "alice" {
		t.Errorf("SessionFrom username = %q", rec.Body)
	}
	if SessionFrom(context.Background()) != nil {
		t.Error("SessionFrom on an unguarded context must be nil")
	}
}

func TestRoleChangeAppliesToLiveSession(t *testing.T) {
	e := newEnv(t)
	c := e.loginAs("mgr", store.RoleManager)
	if rec := e.do("GET", "/test/manager", "", c); rec.Code != 200 {
		t.Fatalf("before = %d", rec.Code)
	}
	if err := e.st.SetRole(context.Background(), "mgr", store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", "/test/manager", "", c); rec.Code != 403 {
		t.Fatalf("after downgrade = %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	c := e.login("bob", "bob-pass")

	rec := e.do("POST", "/api/auth/logout", "", c)
	if rec.Code != 204 {
		t.Fatalf("logout = %d", rec.Code)
	}
	cleared := sessionCookie(rec)
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("cookie not cleared: %+v", cleared)
	}
	if s, _ := e.st.GetSession(context.Background(), c.Value); s != nil {
		t.Error("session still in store")
	}
	if rec = e.do("GET", "/api/auth/me", "", c); rec.Code != 401 {
		t.Errorf("me after logout = %d", rec.Code)
	}

	// Logging out without a session is not an error.
	if rec = e.do("POST", "/api/auth/logout", ""); rec.Code != 204 {
		t.Errorf("anonymous logout = %d", rec.Code)
	}
}

func TestTokenAndCodeShapes(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		tok, err := newToken()
		if err != nil || len(tok) != 64 || seen[tok] {
			t.Fatalf("token %q err=%v dup=%v", tok, err, seen[tok])
		}
		seen[tok] = true
		code, err := newInviteCode()
		if err != nil || len(code) != 22 || strings.ContainsAny(code, "+/=") || seen[code] {
			t.Fatalf("code %q err=%v", code, err)
		}
		seen[code] = true
	}
}
