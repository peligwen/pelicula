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
	"testing"
	"time"

	"pelicula/internal/store"
)

// fakeJF stands in for Identity: alice is a Jellyfin administrator, bob is not.
type fakeJF struct {
	users   map[string]fakeUser
	authErr error // returned by AuthenticateByName when set
}

type fakeUser struct {
	password string
	admin    bool
}

func newFakeJF() *fakeJF {
	return &fakeJF{users: map[string]fakeUser{
		"alice": {password: "alice-pass", admin: true},
		"bob":   {password: "bob-pass"},
	}}
}

func (f *fakeJF) AuthenticateByName(_ context.Context, username, password string) (*LoginResult, error) {
	if f.authErr != nil {
		return nil, f.authErr
	}
	u, ok := f.users[username]
	if !ok || u.password != password {
		return nil, fmt.Errorf("jellyfin: HTTP 401: %w", errors.New("bad login"))
	}
	return &LoginResult{Username: username, IsAdmin: u.admin}, nil
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
	a := New(Deps{Store: st, Jellyfin: jf, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.Handle("GET /test/guarded", a.GuardFunc(whoami))
	mux.Handle("GET /test/guarded2", a.Guard(http.HandlerFunc(whoami)))
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

// login logs the admin in through the real handler and returns the cookie.
func (e *env) login() *http.Cookie {
	e.t.Helper()
	rec := e.do("POST", "/api/auth/login", `{"username":"alice","password":"alice-pass"}`)
	if rec.Code != 200 {
		e.t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c := sessionCookie(rec)
	if c == nil {
		e.t.Fatal("no session cookie")
	}
	return c
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

func TestLoginAdminSetsCookie(t *testing.T) {
	e := newEnv(t)

	rec := e.do("POST", "/api/auth/login", `{"username":"alice","password":"alice-pass"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	decode(t, rec, &got)
	if got["username"] != "alice" || len(got) != 1 {
		t.Fatalf("body = %v", got)
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
	sess, err := e.st.GetSession(context.Background(), c.Value)
	if err != nil || sess == nil || sess.Username != "alice" {
		t.Fatalf("session not stored: %+v %v", sess, err)
	}
}

func TestLoginNonAdminForbidden(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/api/auth/login", `{"username":"bob","password":"bob-pass"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	errorBody(t, rec)
	if sessionCookie(rec) != nil {
		t.Error("cookie set for a non-administrator")
	}
	var n int
	if err := e.st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions stored = %d, want 0 (err %v)", n, err)
	}
}

func TestLoginCustomTTL(t *testing.T) {
	e := newEnv(t)
	e.a = New(Deps{Store: e.st, Jellyfin: e.jf, SessionTTL: time.Hour})
	mux := http.NewServeMux()
	e.a.Routes(mux)
	e.h = mux
	rec := e.do("POST", "/api/auth/login", `{"username":"alice","password":"alice-pass"}`)
	if c := sessionCookie(rec); c == nil || c.MaxAge != 3600 {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestLoginSecureCookieBehindHTTPSProxy(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"alice","password":"alice-pass"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if c := sessionCookie(rec); c == nil || !c.Secure {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestLoginBadPassword(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/api/auth/login", `{"username":"alice","password":"nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	errorBody(t, rec)
	if sessionCookie(rec) != nil {
		t.Error("cookie set on failed login")
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
			rec := e.do("POST", "/api/auth/login", `{"username":"alice","password":"alice-pass"}`)
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

func TestLoginBadRequests(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{``, `{}`, `not json`, `{"username":"alice"}`, `{"password":"x"}`} {
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
	c := e.login()
	rec = e.do("GET", "/api/auth/me", "", c)
	if rec.Code != 200 {
		t.Fatalf("me = %d", rec.Code)
	}
	var got map[string]any
	decode(t, rec, &got)
	if got["username"] != "alice" {
		t.Errorf("me = %v", got)
	}
	rec = e.do("GET", "/api/auth/check", "", c)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Errorf("check = %d body %q", rec.Code, rec.Body)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	e := newEnv(t)
	err := e.st.CreateSession(context.Background(), store.Session{
		Token: "old", Username: "alice", ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Cookie{Name: CookieName, Value: "old"}
	for _, p := range []string{"/api/auth/me", "/api/auth/check", "/test/guarded"} {
		if rec := e.do("GET", p, "", c); rec.Code != 401 {
			t.Errorf("%s = %d, want 401", p, rec.Code)
		}
	}
}

func TestGuard(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/test/guarded", "/test/guarded2"} {
		rec := e.do("GET", p, "")
		if rec.Code != 401 {
			t.Fatalf("%s anonymous = %d, want 401", p, rec.Code)
		}
		if got := errorBody(t, rec); got != "unauthorized" {
			t.Errorf("%s: error = %q", p, got)
		}
	}
	c := e.login()
	for _, p := range []string{"/test/guarded", "/test/guarded2"} {
		rec := e.do("GET", p, "", c)
		if rec.Code != 200 || rec.Body.String() != "alice" {
			t.Errorf("%s with session = %d %q", p, rec.Code, rec.Body)
		}
	}
	if SessionFrom(context.Background()) != nil {
		t.Error("SessionFrom on an unguarded context must be nil")
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	c := e.login()

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

func TestTokenShape(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		tok, err := newToken()
		if err != nil || len(tok) != 64 || seen[tok] {
			t.Fatalf("token %q err=%v dup=%v", tok, err, seen[tok])
		}
		seen[tok] = true
	}
}
