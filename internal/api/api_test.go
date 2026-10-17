package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pelicula/internal/config"
	"pelicula/internal/store"
)

// ── fake Guard: the user comes from a request header ─────────────────────────

// headerGuard stands in for auth.Auth. A request is signed in when it carries
// X-Test-User; anything else is 401, the same as auth.Guard.
type headerGuard struct{}

func (headerGuard) Guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-User") == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ── fake upstreams ───────────────────────────────────────────────────────────

type deleteCall struct {
	ID               int
	RemoveFromClient bool
	Blocklist        bool
}

// fakeArr is an in-memory Sonarr/Radarr that records what the handlers ask of it.
type fakeArr struct {
	mu sync.Mutex

	pingErr     error
	pings       int
	movieLookup []map[string]any // LookupMovie
	byTmdb      []map[string]any // LookupMovieByTmdbID
	seriesList  []map[string]any // LookupSeries
	profiles    []map[string]any
	queue       []map[string]any
	queueErr    error
	movie       map[string]any // GetMovie
	series      map[string]any // GetSeriesByID
	getErr      error
	lookupErr   error
	addResp     map[string]any
	addErr      error
	commandErr  error

	terms     []string
	commands  []map[string]any
	addedMov  []map[string]any
	addedSer  []map[string]any
	deleted   []deleteCall
	gotTmdbID int
}

func (f *fakeArr) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	return f.pingErr
}

func (f *fakeArr) TriggerCommand(_ context.Context, p map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, p)
	return f.commandErr
}

func (f *fakeArr) GetMovie(context.Context, int) (map[string]any, error) { return f.movie, f.getErr }
func (f *fakeArr) GetSeriesByID(context.Context, int) (map[string]any, error) {
	return f.series, f.getErr
}

func (f *fakeArr) LookupMovie(_ context.Context, term string) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terms = append(f.terms, term)
	return f.movieLookup, f.lookupErr
}

func (f *fakeArr) LookupMovieByTmdbID(_ context.Context, id int) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotTmdbID = id
	return f.byTmdb, f.lookupErr
}

func (f *fakeArr) LookupSeries(_ context.Context, term string) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terms = append(f.terms, term)
	return f.seriesList, f.lookupErr
}

func (f *fakeArr) AddMovie(_ context.Context, p map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addedMov = append(f.addedMov, p)
	return f.addResp, f.addErr
}

func (f *fakeArr) AddSeries(_ context.Context, p map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addedSer = append(f.addedSer, p)
	return f.addResp, f.addErr
}

func (f *fakeArr) GetQualityProfiles(context.Context) ([]map[string]any, error) {
	return f.profiles, nil
}

func (f *fakeArr) GetAllQueueRecords(context.Context) ([]map[string]any, error) {
	return f.queue, f.queueErr
}

func (f *fakeArr) DeleteQueueItem(_ context.Context, id int, removeFromClient, blocklist bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, deleteCall{id, removeFromClient, blocklist})
	return nil
}

type fakeQBT struct {
	mu       sync.Mutex
	torrents []Torrent
	transfer *TransferInfo
	listErr  error
	pingErr  error

	stopped, started []string
	deleted          []string
	deletedFiles     []bool
}

func (q *fakeQBT) Ping(context.Context) error { return q.pingErr }
func (q *fakeQBT) ListTorrents(context.Context) ([]Torrent, error) {
	return q.torrents, q.listErr
}
func (q *fakeQBT) GetTransferInfo(context.Context) (*TransferInfo, error) {
	return q.transfer, nil
}
func (q *fakeQBT) StopTorrent(_ context.Context, h string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = append(q.stopped, h)
	return nil
}
func (q *fakeQBT) StartTorrent(_ context.Context, h string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.started = append(q.started, h)
	return nil
}
func (q *fakeQBT) DeleteTorrent(_ context.Context, h string, files bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deleted = append(q.deleted, h)
	q.deletedFiles = append(q.deletedFiles, files)
	return nil
}

type fakeJellyfin struct {
	pingErr error
}

func (j *fakeJellyfin) Ping(context.Context) error { return j.pingErr }

type fakeGluetun struct {
	pingErr error
	tunnel  string
	ip      *VPNStatus
	port    int
}

func (g *fakeGluetun) Ping(context.Context) error { return g.pingErr }
func (g *fakeGluetun) GetPublicIP(context.Context) (*VPNStatus, error) {
	return g.ip, nil
}
func (g *fakeGluetun) GetForwardedPort(context.Context) (int, error) { return g.port, nil }
func (g *fakeGluetun) GetTunnelStatus(context.Context) (string, error) {
	return g.tunnel, nil
}

// ── harness ──────────────────────────────────────────────────────────────────

type env struct {
	t        *testing.T
	srv      *Server
	mux      *http.ServeMux
	store    *store.Store
	sonarr   *fakeArr
	radarr   *fakeArr
	jellyfin *fakeJellyfin
	kicks    int
}

// newEnv builds a Server over an in-memory store with VPN off.
func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{
		t:        t,
		store:    st,
		sonarr:   &fakeArr{},
		radarr:   &fakeArr{},
		jellyfin: &fakeJellyfin{},
	}
	e.srv = &Server{
		Cfg: config.Config{
			Version:       "test-1.0",
			WebhookSecret: "s3cret",
			MoviesPath:    "/media/movies",
			TVPath:        "/media/tv",
			TZ:            "UTC",
		},
		Store:    st,
		Auth:     headerGuard{},
		Sonarr:   e.sonarr,
		Radarr:   e.radarr,
		Jellyfin: e.jellyfin,
		Wired:    func() bool { return true },
		Kick:     func() { e.kicks++ },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.mux = http.NewServeMux()
	e.srv.Routes(e.mux)
	return e
}

type who struct {
	user string
}

var (
	anon  = who{}
	admin = who{"adam"}
)

// do sends a request through the mux as w. body may be nil, a string (sent
// verbatim) or any JSON-marshalable value.
func (e *env) do(w who, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, rd)
	if w.user != "" {
		req.Header.Set("X-Test-User", w.user)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// decode unmarshals a recorder body into a generic map.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, rec.Body.String())
	}
	return m
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, code, rec.Body.String())
	}
}

// jm parses a JSON object literal into the map shape the real clients return.
func jm(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad JSON literal: %v\n%s", err, s)
	}
	return m
}

func list(t *testing.T, items ...string) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, len(items))
	for _, s := range items {
		out = append(out, jm(t, s))
	}
	return out
}

// ── cross-cutting tests ──────────────────────────────────────────────────────

func TestHealth(t *testing.T) {
	e := newEnv(t)
	rec := e.do(anon, "GET", "/api/health", nil)
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["ok"] != true || got["wired"] != true || got["version"] != "test-1.0" {
		t.Fatalf("health = %v", got)
	}

	e.srv.Wired = nil // autowire not running yet
	got = decode(t, e.do(anon, "GET", "/api/health", nil))
	if got["wired"] != false {
		t.Fatalf("wired with nil func = %v, want false", got["wired"])
	}
}

func TestRoutesRequireSession(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ method, path string }{
		{"GET", "/api/status"},
		{"GET", "/api/search?q=x"},
		{"POST", "/api/search/add"},
		{"GET", "/api/downloads"},
		{"POST", "/api/downloads/abc/pause"},
		{"POST", "/api/downloads/abc/resume"},
		{"DELETE", "/api/downloads/abc"},
		{"GET", "/api/jobs"},
		{"POST", "/api/jobs/1/retry"},
		{"GET", "/api/settings"},
		{"PUT", "/api/settings"},
	}
	for _, c := range cases {
		if got := e.do(anon, c.method, c.path, nil).Code; got != 401 {
			t.Errorf("%s %s anonymous = %d, want 401", c.method, c.path, got)
		}
		if got := e.do(admin, c.method, c.path, nil).Code; got == 401 || got == 403 {
			t.Errorf("%s %s signed in = %d, want allowed", c.method, c.path, got)
		}
	}
}

func TestNilAuthFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.srv.Auth = nil
	mux := http.NewServeMux()
	e.srv.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/jobs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
