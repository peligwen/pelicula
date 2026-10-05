package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pelicula/internal/store"
)

type inviteResp struct {
	Code      string    `json:"code"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
	Path      string    `json:"path"`
}

func (e *env) createInvite(admin *http.Cookie, body string) inviteResp {
	e.t.Helper()
	rec := e.do("POST", "/api/invites", body, admin)
	if rec.Code != 201 {
		e.t.Fatalf("create invite = %d %s", rec.Code, rec.Body)
	}
	var r inviteResp
	decode(e.t, rec, &r)
	return r
}

func TestInviteLifecycleAsAdmin(t *testing.T) {
	e := newEnv(t)
	admin := e.login("alice", "alice-pass")

	before := time.Now()
	inv := e.createInvite(admin, `{"role":"manager","expires_hours":24}`)
	if len(inv.Code) != 22 || inv.Role != "manager" || inv.Path != "/register?code="+inv.Code {
		t.Fatalf("created = %+v", inv)
	}
	if d := inv.ExpiresAt.Sub(before); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("expires in %v, want ~24h", d)
	}

	rec := e.do("GET", "/api/invites", "", admin)
	if rec.Code != 200 {
		t.Fatalf("list = %d", rec.Code)
	}
	var list struct {
		Invites []struct {
			Code      string     `json:"code"`
			Role      string     `json:"role"`
			CreatedBy string     `json:"created_by"`
			CreatedAt time.Time  `json:"created_at"`
			ExpiresAt time.Time  `json:"expires_at"`
			UsedBy    string     `json:"used_by"`
			UsedAt    *time.Time `json:"used_at"`
		} `json:"invites"`
	}
	decode(t, rec, &list)
	if len(list.Invites) != 1 {
		t.Fatalf("invites = %+v", list.Invites)
	}
	got := list.Invites[0]
	if got.Code != inv.Code || got.Role != "manager" || got.CreatedBy != "alice" || got.UsedBy != "" || got.UsedAt != nil || got.CreatedAt.IsZero() {
		t.Errorf("listed = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"used_at":null`) {
		t.Errorf("unused invite should render used_at:null: %s", rec.Body)
	}

	if rec = e.do("DELETE", "/api/invites/"+inv.Code, "", admin); rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec = e.do("DELETE", "/api/invites/"+inv.Code, "", admin); rec.Code != 404 {
		t.Errorf("second delete = %d, want 404", rec.Code)
	} else {
		errorBody(t, rec)
	}
	rec = e.do("GET", "/api/invites", "", admin)
	if !strings.Contains(rec.Body.String(), `"invites":[]`) {
		t.Errorf("empty list must be [], got %s", rec.Body)
	}
}

func TestInviteDefaultsAndLimits(t *testing.T) {
	e := newEnv(t)
	admin := e.login("alice", "alice-pass")

	// Empty body: viewer, 72h.
	inv := e.createInvite(admin, ``)
	if inv.Role != "viewer" {
		t.Errorf("role = %q", inv.Role)
	}
	if d := time.Until(inv.ExpiresAt); d < 71*time.Hour || d > 73*time.Hour {
		t.Errorf("default expiry = %v", d)
	}

	// Capped at 720 hours.
	inv = e.createInvite(admin, `{"expires_hours":100000}`)
	if d := time.Until(inv.ExpiresAt); d < 719*time.Hour || d > 721*time.Hour {
		t.Errorf("capped expiry = %v", d)
	}

	for _, body := range []string{`{"role":"god"}`, `{"expires_hours":-1}`, `{bad`} {
		if rec := e.do("POST", "/api/invites", body, admin); rec.Code != 400 {
			t.Errorf("body %q = %d, want 400", body, rec.Code)
		}
	}
}

func TestInvitesForbiddenForNonAdmins(t *testing.T) {
	e := newEnv(t)
	admin := e.login("alice", "alice-pass")
	inv := e.createInvite(admin, `{}`)

	for name, c := range map[string]*http.Cookie{
		"viewer":  e.loginAs("bob", store.RoleViewer),
		"manager": e.loginAs("mgr", store.RoleManager),
	} {
		for _, rc := range []struct{ method, path, body string }{
			{"GET", "/api/invites", ""},
			{"POST", "/api/invites", `{}`},
			{"DELETE", "/api/invites/" + inv.Code, ""},
		} {
			rec := e.do(rc.method, rc.path, rc.body, c)
			if rec.Code != 403 {
				t.Errorf("%s %s %s = %d, want 403", name, rc.method, rc.path, rec.Code)
			}
		}
	}
	// Anonymous.
	if rec := e.do("GET", "/api/invites", ""); rec.Code != 401 {
		t.Errorf("anonymous list = %d", rec.Code)
	}
	// Nothing was deleted by the forbidden attempts.
	if got, _ := e.st.GetInvite(context.Background(), inv.Code); got == nil {
		t.Error("invite deleted by non-admin")
	}
}

func (e *env) register(code, user, pass string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/api/register", fmt.Sprintf(`{"code":%q,"username":%q,"password":%q}`, code, user, pass))
}

func TestRegisterCheck(t *testing.T) {
	e := newEnv(t)
	admin := e.login("alice", "alice-pass")
	inv := e.createInvite(admin, `{"role":"manager"}`)

	check := func(code string) (bool, string) {
		rec := e.do("GET", "/api/register/"+code, "")
		if rec.Code != 200 {
			t.Fatalf("check %q = %d", code, rec.Code)
		}
		var r struct {
			Valid bool   `json:"valid"`
			Role  string `json:"role"`
		}
		decode(t, rec, &r)
		return r.Valid, r.Role
	}
	if v, role := check(inv.Code); !v || role != "manager" {
		t.Errorf("valid invite = %v %q", v, role)
	}
	if v, _ := check("nope"); v {
		t.Error("unknown code reported valid")
	}
	if v, _ := check(strings.Repeat("x", 500)); v {
		t.Error("oversized code reported valid")
	}

	// Expired and used invites are not valid.
	ctx := context.Background()
	if err := e.st.CreateInvite(ctx, store.Invite{Code: "expired", Role: store.RoleViewer, CreatedBy: "alice", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if v, _ := check("expired"); v {
		t.Error("expired invite reported valid")
	}
	if rec := e.register(inv.Code, "carol", "long-enough"); rec.Code != 201 {
		t.Fatalf("register = %d", rec.Code)
	}
	if v, _ := check(inv.Code); v {
		t.Error("used invite reported valid")
	}
}

func TestRegisterHappyPath(t *testing.T) {
	e := newEnv(t)
	admin := e.login("alice", "alice-pass")
	inv := e.createInvite(admin, `{"role":"manager"}`)

	rec := e.register(inv.Code, "carol", "correct horse")
	if rec.Code != 201 {
		t.Fatalf("register = %d %s", rec.Code, rec.Body)
	}
	var got struct{ Username, Role string }
	decode(t, rec, &got)
	if got.Username != "carol" || got.Role != "manager" {
		t.Fatalf("body = %+v", got)
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("registration did not log the user in")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v", c)
	}

	if len(e.jf.created) != 1 || e.jf.created[0] != "carol" {
		t.Errorf("jellyfin users created = %v", e.jf.created)
	}
	ctx := context.Background()
	if role, ok, _ := e.st.GetRole(ctx, "carol"); !ok || role != store.RoleManager {
		t.Errorf("stored role = %q ok=%v", role, ok)
	}
	stored, _ := e.st.GetInvite(ctx, inv.Code)
	if stored == nil || stored.UsedBy != "carol" || !stored.Used() {
		t.Errorf("invite = %+v", stored)
	}

	// The cookie is a working session with the invite's role.
	me := e.do("GET", "/api/auth/me", "", c)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"manager"`) {
		t.Errorf("me = %d %s", me.Code, me.Body)
	}
	if rec := e.do("GET", "/test/manager", "", c); rec.Code != 200 {
		t.Errorf("manager route = %d", rec.Code)
	}
	// And the password works for a regular login.
	e.login("carol", "correct horse")
}

func TestRegisterBadCode(t *testing.T) {
	e := newEnv(t)
	for _, code := range []string{"", "does-not-exist", strings.Repeat("a", 300)} {
		rec := e.register(code, "carol", "long-enough")
		if rec.Code != http.StatusGone {
			t.Errorf("code %q = %d, want 410", code, rec.Code)
		}
		errorBody(t, rec)
	}
	if len(e.jf.created) != 0 {
		t.Errorf("jellyfin user created for a bad code: %v", e.jf.created)
	}
}

func TestRegisterExpiredInvite(t *testing.T) {
	e := newEnv(t)
	err := e.st.CreateInvite(context.Background(), store.Invite{Code: "old", Role: store.RoleViewer, CreatedBy: "alice", ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.register("old", "carol", "long-enough"); rec.Code != http.StatusGone {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRegisterUsedTwice(t *testing.T) {
	e := newEnv(t)
	inv := e.createInvite(e.login("alice", "alice-pass"), `{}`)
	if rec := e.register(inv.Code, "carol", "long-enough"); rec.Code != 201 {
		t.Fatalf("first = %d", rec.Code)
	}
	rec := e.register(inv.Code, "dave", "long-enough")
	if rec.Code != http.StatusGone {
		t.Fatalf("second = %d, want 410", rec.Code)
	}
	if len(e.jf.created) != 1 {
		t.Errorf("second registration created a Jellyfin user: %v", e.jf.created)
	}
}

func TestRegisterWeakPasswordAndBadUsername(t *testing.T) {
	e := newEnv(t)
	inv := e.createInvite(e.login("alice", "alice-pass"), `{}`)

	if rec := e.register(inv.Code, "carol", "short"); rec.Code != 400 {
		t.Errorf("weak password = %d, want 400", rec.Code)
	} else {
		errorBody(t, rec)
	}
	for _, name := range []string{"ab", strings.Repeat("a", 33), "has space", "semi;colon", "slash/name", ""} {
		if rec := e.register(inv.Code, name, "long-enough"); rec.Code != 400 {
			t.Errorf("username %q = %d, want 400", name, rec.Code)
		}
	}
	// None of that consumed the invite or touched Jellyfin.
	if got, _ := e.st.GetInvite(context.Background(), inv.Code); got == nil || got.Used() {
		t.Errorf("invite consumed by rejected requests: %+v", got)
	}
	if len(e.jf.created) != 0 {
		t.Errorf("created = %v", e.jf.created)
	}
	if rec := e.register(inv.Code, "a.b_c-9", "long-enough"); rec.Code != 201 {
		t.Errorf("valid punctuation username = %d", rec.Code)
	}
}

func TestRegisterUsernameTaken(t *testing.T) {
	e := newEnv(t)
	inv := e.createInvite(e.login("alice", "alice-pass"), `{}`)

	rec := e.register(inv.Code, "bob", "long-enough") // bob exists in the fake
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	errorBody(t, rec)
	if got, _ := e.st.GetInvite(context.Background(), inv.Code); got == nil || got.Used() {
		t.Errorf("invite consumed by a failed registration: %+v", got)
	}
	// The invite is still good for another name.
	if rec := e.register(inv.Code, "carol", "long-enough"); rec.Code != 201 {
		t.Errorf("retry = %d", rec.Code)
	}

	// 409 text from Jellyfin is a conflict too.
	inv = e.createInvite(e.login("alice", "alice-pass"), `{}`)
	e.jf.createFn = func(string) error { return fmt.Errorf("jellyfin: HTTP 409: conflict") }
	if rec := e.register(inv.Code, "dave", "long-enough"); rec.Code != 409 {
		t.Errorf("409 text = %d", rec.Code)
	}
	e.jf.createFn = func(string) error { return ErrUserExists }
	if rec := e.register(inv.Code, "dave", "long-enough"); rec.Code != 409 {
		t.Errorf("ErrUserExists = %d", rec.Code)
	}
}

func TestRegisterJellyfinUnavailable(t *testing.T) {
	e := newEnv(t)
	inv := e.createInvite(e.login("alice", "alice-pass"), `{}`)

	e.jf.tokenErr = fmt.Errorf("connection refused")
	if rec := e.register(inv.Code, "carol", "long-enough"); rec.Code != 503 {
		t.Errorf("token failure = %d, want 503", rec.Code)
	}
	e.jf.tokenErr = nil
	e.jf.createFn = func(string) error { return fmt.Errorf("jellyfin: HTTP 500: boom") }
	if rec := e.register(inv.Code, "carol", "long-enough"); rec.Code != 503 {
		t.Errorf("create failure = %d, want 503", rec.Code)
	}
	if got, _ := e.st.GetInvite(context.Background(), inv.Code); got == nil || got.Used() {
		t.Errorf("invite consumed: %+v", got)
	}
}

func TestRegisterLoginFailureStillCreated(t *testing.T) {
	e := newEnv(t)
	inv := e.createInvite(e.login("alice", "alice-pass"), `{}`)
	e.jf.createFn = func(string) error {
		e.jf.authErr = fmt.Errorf("connection refused") // Jellyfin goes away right after creating the user
		return nil
	}
	rec := e.register(inv.Code, "carol", "long-enough")
	if rec.Code != 201 {
		t.Fatalf("status = %d", rec.Code)
	}
	if sessionCookie(rec) != nil {
		t.Error("cookie set although login failed")
	}
}

func TestRegisterConcurrentSameInvite(t *testing.T) {
	e := newEnv(t)
	inv := e.createInvite(e.login("alice", "alice-pass"), `{}`)

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.register(inv.Code, fmt.Sprintf("user%d", i), "long-enough").Code
		}()
	}
	wg.Wait()
	created, gone := 0, 0
	for _, c := range codes {
		switch c {
		case 201:
			created++
		case 410:
			gone++
		}
	}
	if created != 1 || gone != n-1 {
		t.Fatalf("codes = %v", codes)
	}
	if len(e.jf.created) != 1 {
		t.Errorf("jellyfin users created = %v", e.jf.created)
	}
}
