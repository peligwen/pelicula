package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const goodID = "0123456789abcdef0123456789abcdef"

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type rec struct {
	Method, Path, Query, Auth, Body string
}

// fake is a Jellyfin stand-in: handlers keyed by "METHOD /path", every request recorded.
type fake struct {
	mu       sync.Mutex
	reqs     []rec
	handlers map[string]http.HandlerFunc
}

func newFake(t *testing.T, handlers map[string]http.HandlerFunc) (*Client, *fake) {
	t.Helper()
	f := &fake{handlers: handlers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b)) // handlers may read it again
		f.mu.Lock()
		f.reqs = append(f.reqs, rec{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Emby-Authorization"), string(b)})
		f.mu.Unlock()
		if h, ok := f.handlers[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL + "/jellyfin"), f
}

func (f *fake) last() rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *fake) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(s)) }
}

func noContent(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }

func authOK(token string, admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["Username"] != "alice" || in["Pw"] != "secret" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		out := map[string]any{
			"AccessToken": token,
			"User":        map[string]any{"Id": goodID, "Name": "Alice", "Policy": map[string]any{"IsAdministrator": admin}},
		}
		json.NewEncoder(w).Encode(out)
	}
}

func TestEmbyAuthHeader(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{"GET /jellyfin/Users": body(`[]`)})
	if _, err := c.Get(context.Background(), "/Users", ""); err != nil {
		t.Fatal(err)
	}
	want := `MediaBrowser Client="Pelicula", Device="pelicula-dashboard", DeviceId="pelicula-dashboard", Version="1.0"`
	if got := f.last().Auth; got != want {
		t.Fatalf("no token: header = %q", got)
	}
	if _, err := c.Get(context.Background(), "/Users", "tok123"); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Auth; got != want+`, Token="tok123"` {
		t.Fatalf("with token: header = %q", got)
	}

	srv := c.ForDevice(DeviceServer)
	if _, err := srv.Get(context.Background(), "/Users", "tok123"); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Auth; !strings.Contains(got, `DeviceId="pelicula-server"`) || strings.Contains(got, "dashboard") {
		t.Fatalf("ForDevice header = %q", got)
	}
	if c.DeviceID != DeviceDashboard {
		t.Fatalf("ForDevice must copy, original DeviceID = %q", c.DeviceID)
	}
}

// The admin logs in and is used on its own device: Jellyfin logs an existing
// session out when the same user logs in again with the same device id, so a
// shared id would let an admin's dashboard login revoke the server's token.
func TestAdmin_UsesServerDevice(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{
		"POST /jellyfin/Users/AuthenticateByName": authOK("tok", true),
		"GET /jellyfin/Users":                     body(`[]`),
	})
	a := NewAdmin(c, "alice", "secret")
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Auth; !strings.Contains(got, `DeviceId="pelicula-server"`) {
		t.Fatalf("admin login header = %q", got)
	}
	if a.Client().DeviceID != DeviceServer {
		t.Fatalf("Client().DeviceID = %q", a.Client().DeviceID)
	}
	if _, err := a.Client().ListUsers(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Auth; !strings.Contains(got, `DeviceId="pelicula-server"`) {
		t.Fatalf("admin call header = %q", got)
	}
	// The dashboard client is untouched.
	if _, err := c.Get(context.Background(), "/Users", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Auth; !strings.Contains(got, `DeviceId="pelicula-dashboard"`) {
		t.Fatalf("dashboard header = %q", got)
	}
}

func TestDo_PayloadAndHTTPError(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{
		"POST /jellyfin/x": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
			}
			http.Error(w, "boom", http.StatusInternalServerError)
		},
	})
	b, err := c.Post(context.Background(), "/x", "t", map[string]int{"a": 1})
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 500 || !strings.Contains(string(b), "boom") {
		t.Fatalf("b=%q err=%v", b, err)
	}
	if f.last().Body != `{"a":1}` {
		t.Fatalf("body = %q", f.last().Body)
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatal("500 must not match ErrUnauthorized")
	}
}

func TestHTTPError401MatchesErrUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	_, err := New(srv.URL).Get(context.Background(), "/Users", "stale")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthenticateByName(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": authOK("tok", true)})
	res, err := c.AuthenticateByName(context.Background(), "alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if *res != (AuthResult{Token: "tok", UserID: goodID, Username: "Alice", IsAdmin: true}) {
		t.Fatalf("res = %+v", *res)
	}
	if !strings.HasPrefix(f.last().Auth, "MediaBrowser ") || strings.Contains(f.last().Auth, "Token=") {
		t.Fatalf("login must not carry a token: %q", f.last().Auth)
	}

	c, _ = newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": authOK("tok", false)})
	res, err = c.AuthenticateByName(context.Background(), "alice", "secret")
	if err != nil || res.IsAdmin {
		t.Fatalf("non-admin: %+v %v", res, err)
	}
}

func TestAuthenticateByName_Failures(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": authOK("tok", true)})
	if _, err := c.AuthenticateByName(context.Background(), "alice", "wrong"); err != ErrUnauthorized {
		t.Fatalf("bad password: err = %v, want ErrUnauthorized", err)
	}

	c, _ = newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}})
	_, err := c.AuthenticateByName(context.Background(), "alice", "secret")
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 503 || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("503: err = %v", err)
	}

	c, _ = newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": body(`{"User":{}}`)})
	if _, err := c.AuthenticateByName(context.Background(), "alice", "secret"); err == nil {
		t.Fatal("incomplete response must error")
	}

	if _, err := New("http://127.0.0.1:1").AuthenticateByName(context.Background(), "a", "b"); err == nil {
		t.Fatal("unreachable server must error")
	}
}

func TestPingAndWizardStatus(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{"GET /jellyfin/System/Info/Public": body(`{"StartupWizardCompleted":true,"Version":"10.11"}`)})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	done, err := c.StartupWizardCompleted(context.Background())
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}

	c, _ = newFake(t, map[string]http.HandlerFunc{"GET /jellyfin/System/Info/Public": body(`{"StartupWizardCompleted":false}`)})
	if done, err := c.StartupWizardCompleted(context.Background()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}

	c, _ = newFake(t, nil)
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("404 must fail Ping")
	}
	if _, err := c.StartupWizardCompleted(context.Background()); err == nil {
		t.Fatal("404 must fail StartupWizardCompleted")
	}
}

func TestCompleteStartupWizard(t *testing.T) {
	wizardSettle = time.Millisecond
	var order []string
	step := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			order = append(order, name)
			w.WriteHeader(http.StatusNoContent)
		}
	}
	c, f := newFake(t, map[string]http.HandlerFunc{
		"POST /jellyfin/Startup/Configuration": step("config"),
		"GET /jellyfin/Startup/User":           step("get-user"),
		"POST /jellyfin/Startup/User":          step("user"),
		"POST /jellyfin/Startup/Complete":      step("complete"),
	})
	if err := c.CompleteStartupWizard(context.Background(), "admin", "pw123456"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "config,get-user,user,complete" {
		t.Fatalf("order = %v", order)
	}
	for _, r := range f.reqs {
		if r.Path == "/jellyfin/Startup/User" && r.Method == "POST" {
			var in map[string]string
			json.Unmarshal([]byte(r.Body), &in)
			if in["Name"] != "admin" || in["Password"] != "pw123456" {
				t.Fatalf("user body = %s", r.Body)
			}
		}
	}
}

func TestCompleteStartupWizard_Errors(t *testing.T) {
	wizardSettle = time.Millisecond
	c, _ := newFake(t, nil)
	if err := c.CompleteStartupWizard(context.Background(), "", "pw"); err == nil {
		t.Fatal("empty admin must error")
	}
	// Config step fails.
	if err := c.CompleteStartupWizard(context.Background(), "admin", "pw"); err == nil {
		t.Fatal("expected error from 404 config step")
	}
	// A failing GET /Startup/User is tolerated; a failing POST is not.
	c, _ = newFake(t, map[string]http.HandlerFunc{
		"POST /jellyfin/Startup/Configuration": noContent,
		"POST /jellyfin/Startup/User":          func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 400) },
	})
	err := c.CompleteStartupWizard(context.Background(), "admin", "pw")
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 400 {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteStartupWizard_ContextCancelDuringSettle(t *testing.T) {
	wizardSettle = time.Hour
	defer func() { wizardSettle = 2 * time.Second }()
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"POST /jellyfin/Startup/Configuration": noContent,
		"GET /jellyfin/Startup/User":           noContent,
		"POST /jellyfin/Startup/User":          noContent,
		"POST /jellyfin/Startup/Complete":      noContent,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.CompleteStartupWizard(ctx, "admin", "pw"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestListUsers(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{"GET /jellyfin/Users": body(`[
		{"Id":"` + goodID + `","Name":"alice","LastLoginDate":"2026-01-02T03:04:05Z","Policy":{"IsAdministrator":true,"IsDisabled":false}},
		{"Id":"b","Name":"bob","Policy":{"IsAdministrator":false,"IsDisabled":true}}]`)})
	users, err := c.ListUsers(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	want := []User{
		{ID: goodID, Name: "alice", IsAdmin: true, LastLogin: "2026-01-02T03:04:05Z"},
		{ID: "b", Name: "bob", IsDisabled: true},
	}
	if len(users) != 2 || users[0] != want[0] || users[1] != want[1] {
		t.Fatalf("users = %+v", users)
	}
	if !strings.HasSuffix(f.last().Auth, `Token="tok"`) {
		t.Fatalf("auth = %q", f.last().Auth)
	}
}

func TestListUsers_Unauthorized(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{"GET /jellyfin/Users": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }})
	_, err := c.ListUsers(context.Background(), "stale")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateUser(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{
		"GET /jellyfin/Users":      body(`[{"Id":"` + goodID + `","Name":"alice"}]`),
		"POST /jellyfin/Users/New": body(`{"Id":"fedcba9876543210fedcba9876543210","Name":"bob"}`),
		"POST /jellyfin/Users/fedcba9876543210fedcba9876543210/Password": noContent,
	})
	id, err := c.CreateUser(context.Background(), "tok", "bob", "hunter2hunter2")
	if err != nil || id != "fedcba9876543210fedcba9876543210" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	var newBody, pwBody string
	for _, r := range f.reqs {
		switch r.Path {
		case "/jellyfin/Users/New":
			newBody = r.Body
		case "/jellyfin/Users/fedcba9876543210fedcba9876543210/Password":
			pwBody = r.Body
		}
	}
	if newBody != `{"Name":"bob"}` || pwBody != `{"CurrentPw":"","NewPw":"hunter2hunter2"}` {
		t.Fatalf("new=%s pw=%s", newBody, pwBody)
	}
}

func TestCreateUser_ExistingName(t *testing.T) {
	// Pre-check hit (case-insensitive).
	c, f := newFake(t, map[string]http.HandlerFunc{"GET /jellyfin/Users": body(`[{"Id":"` + goodID + `","Name":"Alice"}]`)})
	_, err := c.CreateUser(context.Background(), "tok", "alice", "pw123456")
	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("err = %v", err)
	}
	if f.count("POST", "/jellyfin/Users/New") != 0 {
		t.Fatal("must not POST when the name is taken")
	}

	// Race / failed pre-check: Jellyfin answers with its plain-text 400.
	c, _ = newFake(t, map[string]http.HandlerFunc{
		"POST /jellyfin/Users/New": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "A user with the name 'bob' already exists.", http.StatusBadRequest)
		},
	})
	_, err = c.CreateUser(context.Background(), "tok", "bob", "pw123456")
	var he *HTTPError
	if !errors.Is(err, ErrUserExists) || !errors.As(err, &he) || he.StatusCode != 400 {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateUser_PasswordFailureRollsBack(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{
		"GET /jellyfin/Users":      body(`[]`),
		"POST /jellyfin/Users/New": body(`{"Id":"` + goodID + `"}`),
		"POST /jellyfin/Users/" + goodID + "/Password": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "weak", http.StatusBadRequest)
		},
		"DELETE /jellyfin/Users/" + goodID: noContent,
	})
	_, err := c.CreateUser(context.Background(), "tok", "bob", "pw123456")
	if err == nil || !strings.Contains(err.Error(), "user removed") {
		t.Fatalf("err = %v", err)
	}
	if f.count("DELETE", "/jellyfin/Users/"+goodID) != 1 {
		t.Fatal("expected rollback delete")
	}
}

func TestCreateUser_BadInput(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"GET /jellyfin/Users":      body(`[]`),
		"POST /jellyfin/Users/New": body(`{"Id":"../etc"}`),
	})
	if _, err := c.CreateUser(context.Background(), "tok", "", "pw"); err == nil {
		t.Fatal("empty name must error")
	}
	if _, err := c.CreateUser(context.Background(), "tok", "bob", ""); err == nil {
		t.Fatal("empty password must error")
	}
	if _, err := c.CreateUser(context.Background(), "tok", "bob", "pw123456"); err == nil {
		t.Fatal("malformed user id must error")
	}
}

func TestDeleteUser(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{"DELETE /jellyfin/Users/" + goodID: noContent})
	if err := c.DeleteUser(context.Background(), "tok", goodID); err != nil {
		t.Fatal(err)
	}
	if f.last().Method != "DELETE" {
		t.Fatalf("last = %+v", f.last())
	}
	if err := c.DeleteUser(context.Background(), "tok", "../../System/Restart"); err == nil {
		t.Fatal("path traversal id must be rejected")
	}
	n := len(f.reqs)
	_ = c.DeleteUser(context.Background(), "tok", "nope")
	if len(f.reqs) != n {
		t.Fatal("invalid id must not reach the server")
	}
	// Unknown (valid-looking) user: Jellyfin 404.
	err := c.DeleteUser(context.Background(), "tok", "ffffffffffffffffffffffffffffffff")
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 404 {
		t.Fatalf("err = %v", err)
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		goodID:                                 true,
		"01234567-89ab-cdef-0123-456789abcdef": true,
		"":                                     false,
		"short":                                false,
		strings.Repeat("g", 32):                false,
		"01234567_89ab_cdef_0123_456789abcdef": false,
	} {
		if got := validID(id); got != want {
			t.Errorf("validID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestLibraries(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{
		"GET /jellyfin/Library/VirtualFolders": body(`[
			{"Name":"Movies","CollectionType":"movies","Locations":["/media/movies"]},
			{"Name":"Mixed","Locations":[]}]`),
		"POST /jellyfin/Library/VirtualFolders": noContent,
		"POST /jellyfin/Library/Refresh":        noContent,
	})
	ctx := context.Background()

	libs, err := c.ListLibraries(ctx, "tok")
	if err != nil || len(libs) != 2 {
		t.Fatalf("libs=%+v err=%v", libs, err)
	}
	if libs[0].Name != "Movies" || libs[0].CollectionType != "movies" || len(libs[0].Locations) != 1 || libs[0].Locations[0] != "/media/movies" {
		t.Fatalf("libs[0] = %+v", libs[0])
	}
	if libs[1].CollectionType != "" {
		t.Fatalf("libs[1] = %+v", libs[1])
	}

	if err := c.AddLibrary(ctx, "tok", "TV Shows", "tvshows", "/media/tv"); err != nil {
		t.Fatal(err)
	}
	add := f.last()
	if add.Query != "collectionType=tvshows&name=TV+Shows&paths=%2Fmedia%2Ftv&refreshLibrary=true" {
		t.Fatalf("query = %q", add.Query)
	}
	if !strings.Contains(add.Body, `"Path":"/media/tv"`) {
		t.Fatalf("body = %q", add.Body)
	}

	// A comma in the path would be split server-side, so it only goes in the body.
	if err := c.AddLibrary(ctx, "tok", "X", "movies", "/media/a,b"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.last().Query, "paths=") || !strings.Contains(f.last().Body, `/media/a,b`) {
		t.Fatalf("comma path: %+v", f.last())
	}

	if err := c.RefreshLibrary(ctx, "tok"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.Method != "POST" || got.Path != "/jellyfin/Library/Refresh" {
		t.Fatalf("refresh = %+v", got)
	}
}

func TestLibraries_Errors(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{
		"POST /jellyfin/Library/VirtualFolders": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 500) },
		"POST /jellyfin/Library/Refresh":        func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
		"GET /jellyfin/Library/VirtualFolders":  body(`nope`),
	})
	ctx := context.Background()
	if err := c.AddLibrary(ctx, "t", "n", "movies", "/p"); err == nil {
		t.Fatal("AddLibrary must surface 500")
	}
	if err := c.RefreshLibrary(ctx, "t"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("RefreshLibrary err = %v", err)
	}
	if _, err := c.ListLibraries(ctx, "t"); err == nil {
		t.Fatal("ListLibraries must fail on bad JSON")
	}
}

func TestAdmin_CachesAndInvalidates(t *testing.T) {
	var logins int
	c, f := newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": func(w http.ResponseWriter, r *http.Request) {
		logins++
		authOK("tok"+string(rune('0'+logins)), true)(w, r)
	}})
	a := NewAdmin(c, "alice", "secret")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		tok, err := a.Token(ctx)
		if err != nil || tok != "tok1" {
			t.Fatalf("Token = %q, %v", tok, err)
		}
	}
	if n := f.count("POST", "/jellyfin/Users/AuthenticateByName"); n != 1 {
		t.Fatalf("logins = %d, want 1", n)
	}
	a.Invalidate()
	if tok, err := a.Token(ctx); err != nil || tok != "tok2" {
		t.Fatalf("after Invalidate: %q, %v", tok, err)
	}
}

func TestAdmin_LoginFailureIsNotCached(t *testing.T) {
	c, _ := newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": authOK("tok", true)})
	bad := NewAdmin(c, "alice", "wrong")
	if _, err := bad.Token(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	bad.password = "secret"
	if tok, err := bad.Token(context.Background()); err != nil || tok != "tok" {
		t.Fatalf("retry after failure: %q %v", tok, err)
	}
}

func TestAdmin_ConcurrentTokenLogsInOnce(t *testing.T) {
	c, f := newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": authOK("tok", true)})
	a := NewAdmin(c, "alice", "secret")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Token(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.count("POST", "/jellyfin/Users/AuthenticateByName"); n != 1 {
		t.Fatalf("logins = %d, want 1", n)
	}
}

func TestAdmin_WithTokenRetriesOnceOn401(t *testing.T) {
	var logins int
	c, _ := newFake(t, map[string]http.HandlerFunc{"POST /jellyfin/Users/AuthenticateByName": func(w http.ResponseWriter, r *http.Request) {
		logins++
		authOK("tok"+string(rune('0'+logins)), true)(w, r)
	}})
	a := NewAdmin(c, "alice", "secret")

	var seen []string
	err := a.WithToken(context.Background(), func(tok string) error {
		seen = append(seen, tok)
		if tok == "tok1" {
			return &HTTPError{StatusCode: 401}
		}
		return nil
	})
	if err != nil || strings.Join(seen, ",") != "tok1,tok2" {
		t.Fatalf("seen=%v err=%v", seen, err)
	}

	// A persistent 401 is retried only once, then returned.
	calls := 0
	err = a.WithToken(context.Background(), func(string) error { calls++; return ErrUnauthorized })
	if !errors.Is(err, ErrUnauthorized) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	// Other errors are returned as-is without a retry.
	boom := errors.New("boom")
	calls = 0
	if err := a.WithToken(context.Background(), func(string) error { calls++; return boom }); err != boom || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
