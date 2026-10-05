//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	projectName   = "pelicula-test"
	hostPort      = "7399"
	upTimeout     = 15 * time.Minute
	healthTimeout = 5 * time.Minute
	healthPoll    = 5 * time.Second
	jobTimeout    = 2 * time.Minute

	rateLimitRetries = 4
	rateLimitBackoff = 7 * time.Second // the limit is 10 requests a minute: one every 6s
)

// ── API shapes (decoded locally so the test stays a black-box check of the
// documented JSON, not of the Go types behind it) ───────────────────────────

type searchResult struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Poster    string `json:"poster"`
	TmdbID    int    `json:"tmdb_id"`
	TvdbID    int    `json:"tvdb_id"`
	InLibrary bool   `json:"in_library"`
}

type request struct {
	ID          int64  `json:"id"`
	MediaType   string `json:"media_type"`
	Title       string `json:"title"`
	RequestedBy string `json:"requested_by"`
	Status      string `json:"status"`
	ArrID       int    `json:"arr_id"`
	DecidedBy   string `json:"decided_by"`
}

type job struct {
	ID      int64  `json:"id"`
	ArrType string `json:"arr_type"`
	ArrID   int    `json:"arr_id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Status  string `json:"status"`
	Result  string `json:"result"`
	Error   string `json:"error"`
}

type jobResult struct {
	Passed    bool   `json:"passed"`
	Integrity string `json:"integrity"`
	Reason    string `json:"reason"`
}

type userRole struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

// ── stack: the docker compose project under test ────────────────────────────

type stack struct {
	root        string // the repository root
	composeFile string
	envFile     string // empty file: keeps a developer's own .env out of the run
	env         []string
	baseURL     string

	adminUser, adminPass, webhookSecret string
	configDir, libraryDir, workDir      string
}

// compose runs `docker compose` for the test project and returns its combined
// output. Every call goes through here so the project name, compose file and
// environment are identical for up, logs and down.
func (s *stack) compose(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{
		"compose", "-p", projectName, "--env-file", s.envFile, "-f", s.composeFile,
	}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = s.root
	cmd.Env = s.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func requireDocker(t *testing.T) {
	t.Helper()
	fail := t.Skipf
	if os.Getenv("CI") != "" {
		fail = t.Fatalf // never let CI pass by skipping
	}
	if _, err := exec.LookPath("docker"); err != nil {
		fail("docker is not on PATH")
	}
	if out, err := exec.CommandContext(t.Context(), "docker", "compose", "version").CombinedOutput(); err != nil {
		fail("docker compose v2 is not available: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(t.Context(), "docker", "info").CombinedOutput(); err != nil {
		fail("the docker daemon is not reachable: %v\n%s", err, out)
	}
}

func randHex(t *testing.T, nBytes int) string {
	t.Helper()
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newStack prepares temp folders, seeds, and the compose environment. It does
// not start anything.
func newStack(t *testing.T) *stack {
	t.Helper()
	requireDocker(t)

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	composeFile := filepath.Join(root, "compose", "docker-compose.yml")
	if _, err := os.Stat(composeFile); err != nil {
		t.Fatalf("compose file: %v", err)
	}

	base, err := os.MkdirTemp("", "pelicula-e2e-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() {
		// Containers write here as PUID; a leftover is not worth failing over.
		if err := os.RemoveAll(base); err != nil {
			t.Logf("could not remove %s: %v", base, err)
		}
	})
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	s := &stack{
		root:          root,
		composeFile:   composeFile,
		envFile:       filepath.Join(base, "empty.env"),
		baseURL:       "http://127.0.0.1:" + hostPort,
		adminUser:     "admin",
		adminPass:     randHex(t, 8),
		webhookSecret: randHex(t, 16),
		configDir:     filepath.Join(base, "config"),
		libraryDir:    filepath.Join(base, "library"),
		workDir:       filepath.Join(base, "work"),
	}
	writeFile(t, s.envFile, "")

	// Create every bind-mount source as the current user. Docker would
	// otherwise create missing ones as root, and the pelicula container runs
	// as PUID:PGID.
	for _, d := range []string{
		"sonarr", "radarr", "prowlarr", "qbittorrent", "jellyfin", "pelicula", "gluetun",
	} {
		mkdir(t, filepath.Join(s.configDir, d))
	}
	mkdir(t, filepath.Join(s.libraryDir, "movies"))
	mkdir(t, filepath.Join(s.libraryDir, "tv"))
	mkdir(t, filepath.Join(s.workDir, "downloads", "radarr"))
	mkdir(t, filepath.Join(s.workDir, "downloads", "tv-sonarr"))

	// Seed the service configs exactly as `pelicula up` does (the *arr URL
	// bases, Jellyfin's BaseUrl, qBittorrent's subnet whitelist), via the CLI
	// itself so this test never carries its own copy of the seeds.
	seed := exec.CommandContext(t.Context(), "go", "run", "./cmd/pelicula", "seed", s.configDir)
	seed.Dir = root
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("pelicula seed: %v\n%s", err, out)
	}

	s.env = append(os.Environ(),
		"CONFIG_DIR="+s.configDir,
		"LIBRARY_DIR="+s.libraryDir,
		"WORK_DIR="+s.workDir,
		"PELICULA_PORT="+hostPort,
		"PELICULA_VPN=false",
		"PELICULA_VERSION=integration",
		"PUID="+strconv.Itoa(os.Getuid()),
		"PGID="+strconv.Itoa(os.Getgid()),
		"TZ=UTC",
		"WEBHOOK_SECRET="+s.webhookSecret,
		"GLUETUN_HTTP_USER=pelicula",
		"GLUETUN_HTTP_PASS="+randHex(t, 8),
		"JELLYFIN_ADMIN_USER="+s.adminUser,
		"JELLYFIN_PASSWORD="+s.adminPass,
		// Referenced by the compose file but unused: the vpn profile is off.
		"WIREGUARD_PRIVATE_KEY=",
		"SERVER_COUNTRIES=Netherlands",
	)
	return s
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// start builds and starts the stack, and registers cleanup: logs first on
// failure, then `down -v --remove-orphans` unconditionally.
func (s *stack) start(t *testing.T) {
	t.Helper()

	t.Cleanup(func() { // runs last
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if out, err := s.compose(ctx, "down", "-v", "--remove-orphans"); err != nil {
			t.Logf("compose down: %v\n%s", err, out)
		}
	})
	t.Cleanup(func() { // runs first
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if out, err := s.compose(ctx, "ps", "-a"); err == nil {
			t.Logf("docker compose ps -a:\n%s", out)
		}
		out, err := s.compose(ctx, "logs", "--tail", "100", "pelicula")
		if err != nil {
			t.Logf("docker compose logs pelicula: %v", err)
		}
		t.Logf("docker compose logs --tail 100 pelicula:\n%s", out)
	})

	// Clear leftovers from an aborted earlier run of the same project.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	_, _ = s.compose(ctx, "down", "-v", "--remove-orphans")

	upCtx, upCancel := context.WithTimeout(t.Context(), upTimeout)
	defer upCancel()
	if out, err := s.compose(upCtx, "up", "-d", "--build"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// waitWired polls /api/health until it reports ok and wired.
func (s *stack) waitWired(t *testing.T) {
	t.Helper()
	hc := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(healthTimeout)
	last := "no response yet"
	for {
		resp, err := hc.Get(s.baseURL + "/api/health")
		switch {
		case err != nil:
			last = err.Error()
		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			var h struct {
				OK    bool `json:"ok"`
				Wired bool `json:"wired"`
			}
			if resp.StatusCode == http.StatusOK && json.Unmarshal(body, &h) == nil && h.OK && h.Wired {
				return
			}
			last = fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if time.Now().After(deadline) {
			t.Fatalf("stack not wired within %s; last /api/health: %s", healthTimeout, last)
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("interrupted while waiting for /api/health; last: %s", last)
		case <-time.After(healthPoll):
		}
	}
}

// ── HTTP client with its own cookie jar ─────────────────────────────────────

type apiClient struct {
	base string
	hc   *http.Client
}

func newAPIClient(t *testing.T, base string) *apiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &apiClient{base: base, hc: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
}

func (c *apiClient) do(t *testing.T, method, path string, body any, header map[string]string) (int, []byte) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			t.Fatalf("encode %s %s: %v", method, path, err)
		}
	}
	for attempt := 0; ; attempt++ {
		status, data := c.once(t, method, path, payload, header)
		// nginx limits login, register and the invite lookup to 10 requests a
		// minute per IP and answers 429; the server itself never does. Wait
		// out the bucket instead of failing on a limit this test can hit.
		if status != http.StatusTooManyRequests || attempt >= rateLimitRetries {
			return status, data
		}
		t.Logf("%s %s: rate limited by nginx, waiting %s", method, path, rateLimitBackoff)
		select {
		case <-t.Context().Done():
			t.Fatalf("interrupted while rate limited on %s %s", method, path)
		case <-time.After(rateLimitBackoff):
		}
	}
}

func (c *apiClient) once(t *testing.T, method, path string, payload []byte, header map[string]string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, c.base+path, rd)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, data
}

// call requires wantStatus and, when out is non-nil, decodes the JSON body.
func (c *apiClient) call(t *testing.T, method, path string, body any, wantStatus int, out any) {
	t.Helper()
	status, data := c.do(t, method, path, body, nil)
	if status != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, wantStatus, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
}

// expectError requires a status and the {"error":"..."} body format.
func (c *apiClient) expectError(t *testing.T, method, path string, body any, wantStatus int) {
	t.Helper()
	status, data := c.do(t, method, path, body, nil)
	if status != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, wantStatus, strings.TrimSpace(string(data)))
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &e); err != nil || e.Error == "" {
		t.Fatalf(`%s %s: body %q is not {"error":"..."}`, method, path, data)
	}
}

// step runs fn as a subtest and stops the whole test if it fails, since later
// steps build on earlier ones.
func step(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, fn) {
		t.Fatalf("step %q failed; stopping", name)
	}
}

// ── the test ────────────────────────────────────────────────────────────────

func TestStack(t *testing.T) {
	s := newStack(t)
	s.start(t)
	s.waitWired(t)

	admin := newAPIClient(t, s.baseURL)
	viewer := newAPIClient(t, s.baseURL)
	anon := newAPIClient(t, s.baseURL)

	viewerName := "viewer-" + randHex(t, 3) // matches ^[A-Za-z0-9._-]{3,32}$
	viewerPass := randHex(t, 8)

	var (
		inviteCode string
		picked     *searchResult
		reqID      int64
		arrID      = 1 // movie id used by the webhook; replaced by the approved request's arr_id
		movieTitle = "Integration Test Movie"
	)

	step(t, "unauthenticated requests are rejected", func(t *testing.T) {
		anon.expectError(t, http.MethodGet, "/api/status", nil, http.StatusUnauthorized)
		anon.expectError(t, http.MethodGet, "/api/auth/me", nil, http.StatusUnauthorized)
		anon.expectError(t, http.MethodGet, "/api/jobs", nil, http.StatusUnauthorized)
	})

	step(t, "admin login", func(t *testing.T) {
		anon.expectError(t, http.MethodPost, "/api/auth/login",
			map[string]string{"username": s.adminUser, "password": "definitely-wrong"}, http.StatusUnauthorized)

		var me userRole
		admin.call(t, http.MethodPost, "/api/auth/login",
			map[string]string{"username": s.adminUser, "password": s.adminPass}, http.StatusOK, &me)
		if me.Username != s.adminUser || me.Role != "admin" {
			t.Fatalf("login returned %+v, want admin %q", me, s.adminUser)
		}
		admin.call(t, http.MethodGet, "/api/auth/me", nil, http.StatusOK, &me)
		if me.Role != "admin" {
			t.Fatalf("/api/auth/me role = %q, want admin", me.Role)
		}
		admin.call(t, http.MethodGet, "/api/auth/check", nil, http.StatusNoContent, nil)

		var st struct {
			Wired bool `json:"wired"`
			VPN   struct {
				Enabled bool `json:"enabled"`
			} `json:"vpn"`
		}
		admin.call(t, http.MethodGet, "/api/status", nil, http.StatusOK, &st)
		if !st.Wired || st.VPN.Enabled {
			t.Fatalf("/api/status wired=%v vpn.enabled=%v, want wired and no vpn", st.Wired, st.VPN.Enabled)
		}
	})

	step(t, "admin creates an invite", func(t *testing.T) {
		var inv struct {
			Code string `json:"code"`
			Role string `json:"role"`
			Path string `json:"path"`
		}
		admin.call(t, http.MethodPost, "/api/invites",
			map[string]any{"role": "viewer", "expires_hours": 1}, http.StatusCreated, &inv)
		if inv.Code == "" || inv.Role != "viewer" || inv.Path != "/register?code="+inv.Code {
			t.Fatalf("invite = %+v", inv)
		}
		inviteCode = inv.Code

		var list struct {
			Invites []struct {
				Code string `json:"code"`
			} `json:"invites"`
		}
		admin.call(t, http.MethodGet, "/api/invites", nil, http.StatusOK, &list)
		found := false
		for _, i := range list.Invites {
			found = found || i.Code == inviteCode
		}
		if !found {
			t.Fatalf("invite %q missing from GET /api/invites", inviteCode)
		}
	})

	step(t, "invite validates", func(t *testing.T) {
		var v struct {
			Valid bool   `json:"valid"`
			Role  string `json:"role"`
		}
		anon.call(t, http.MethodGet, "/api/register/"+url.PathEscape(inviteCode), nil, http.StatusOK, &v)
		if !v.Valid || v.Role != "viewer" {
			t.Fatalf("register/%s = %+v, want valid viewer", inviteCode, v)
		}
		anon.call(t, http.MethodGet, "/api/register/not-a-real-code", nil, http.StatusOK, &v)
		if v.Valid {
			t.Fatalf("unknown invite reported valid")
		}
	})

	step(t, "viewer registers", func(t *testing.T) {
		var me userRole
		viewer.call(t, http.MethodPost, "/api/register",
			map[string]string{"code": inviteCode, "username": viewerName, "password": viewerPass},
			http.StatusCreated, &me)
		if me.Username != viewerName || me.Role != "viewer" {
			t.Fatalf("register returned %+v, want viewer %q", me, viewerName)
		}
		// Registration signs the new user in.
		viewer.call(t, http.MethodGet, "/api/auth/me", nil, http.StatusOK, &me)
		if me.Username != viewerName {
			t.Fatalf("/api/auth/me = %+v", me)
		}
		// Role limits.
		viewer.expectError(t, http.MethodGet, "/api/settings", nil, http.StatusForbidden)
		viewer.expectError(t, http.MethodGet, "/api/invites", nil, http.StatusForbidden)
		// Single use.
		anon.expectError(t, http.MethodPost, "/api/register",
			map[string]string{"code": inviteCode, "username": viewerName + "x", "password": viewerPass},
			http.StatusGone)
	})

	step(t, "viewer searches", func(t *testing.T) {
		// Search needs Radarr/Sonarr to reach their metadata service. CI without
		// internet cannot, so skip (not fail) unless explicitly required.
		unavailable := func(format string, args ...any) {
			if os.Getenv("PELICULA_E2E_REQUIRE_SEARCH") != "" {
				t.Fatalf(format, args...)
			}
			t.Skipf(format, args...)
		}
		status, data := viewer.do(t, http.MethodGet, "/api/search?q=matrix", nil, nil)
		if status != http.StatusOK {
			unavailable("search unavailable (status %d): %s", status, strings.TrimSpace(string(data)))
		}
		var res struct {
			Results []searchResult `json:"results"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatalf("decode search: %v", err)
		}
		if len(res.Results) == 0 {
			unavailable("search returned no results; metadata lookup is probably offline")
		}
		for i := range res.Results {
			r := &res.Results[i]
			if r.Type == "movie" && r.TmdbID != 0 && !r.InLibrary {
				picked = r
				break
			}
		}
		if picked == nil {
			unavailable("no requestable movie among %d results", len(res.Results))
		}
		t.Logf("picked %q (%d) tmdb=%d", picked.Title, picked.Year, picked.TmdbID)
		movieTitle = picked.Title
	})

	step(t, "viewer requests", func(t *testing.T) {
		if picked == nil {
			t.Skip("no search result to request")
		}
		body := map[string]any{
			"type": "movie", "tmdb_id": picked.TmdbID, "tvdb_id": picked.TvdbID,
			"title": picked.Title, "year": picked.Year, "poster": picked.Poster,
		}
		var r request
		viewer.call(t, http.MethodPost, "/api/requests", body, http.StatusCreated, &r)
		if r.ID == 0 || r.Status != "pending" || r.RequestedBy != viewerName {
			t.Fatalf("request = %+v, want pending by %q", r, viewerName)
		}
		reqID = r.ID

		viewer.expectError(t, http.MethodPost, "/api/requests", body, http.StatusConflict)
		viewer.expectError(t, http.MethodPost, fmt.Sprintf("/api/requests/%d/approve", reqID), nil, http.StatusForbidden)
	})

	step(t, "admin approves", func(t *testing.T) {
		if reqID == 0 {
			t.Skip("no request to approve")
		}
		var r request
		admin.call(t, http.MethodPost, fmt.Sprintf("/api/requests/%d/approve", reqID), nil, http.StatusOK, &r)
		if r.Status != "approved" {
			t.Fatalf("approve returned %+v", r)
		}

		var list struct {
			Requests []request `json:"requests"`
		}
		for who, c := range map[string]*apiClient{"admin": admin, "viewer": viewer} {
			c.call(t, http.MethodGet, "/api/requests", nil, http.StatusOK, &list)
			var got *request
			for i := range list.Requests {
				if list.Requests[i].ID == reqID {
					got = &list.Requests[i]
				}
			}
			if got == nil {
				t.Fatalf("%s: request %d missing from GET /api/requests", who, reqID)
			}
			if got.Status != "approved" || got.ArrID == 0 {
				t.Fatalf("%s: request = %+v, want approved with arr_id", who, *got)
			}
			arrID = got.ArrID
		}
	})

	step(t, "import webhook creates a job", func(t *testing.T) {
		// A real file under LIBRARY_DIR, seen by the server at /media/... An
		// empty file cannot be a valid video, so the pipeline should fail the
		// job with a reason, which still proves webhook -> queue -> worker.
		rel := filepath.Join("movies", "Integration Test (2024)", "Integration Test (2024).mkv")
		writeFile(t, filepath.Join(s.libraryDir, rel), "")
		containerPath := "/media/" + filepath.ToSlash(rel)

		payload := map[string]any{
			"eventType":  "Download",
			"movie":      map[string]any{"id": arrID, "title": movieTitle, "year": 2024, "tmdbId": 603},
			"movieFile":  map[string]any{"path": containerPath, "size": 0},
			"downloadId": "INTEGRATIONTEST" + randHex(t, 4),
		}

		// Wrong or missing secret.
		anon.expectError(t, http.MethodPost, "/api/hooks/import", payload, http.StatusUnauthorized)
		status, _ := anon.do(t, http.MethodPost, "/api/hooks/import", payload,
			map[string]string{"X-Webhook-Secret": "wrong"})
		if status != http.StatusUnauthorized {
			t.Fatalf("wrong secret: status %d, want 401", status)
		}

		// Test event from the *arr "Test" button.
		var ok struct {
			Status string `json:"status"`
		}
		status, data := anon.do(t, http.MethodPost, "/api/hooks/import",
			map[string]any{"eventType": "Test"}, map[string]string{"X-Webhook-Secret": s.webhookSecret})
		if status != http.StatusOK || json.Unmarshal(data, &ok) != nil || ok.Status != "ok" {
			t.Fatalf("Test event: status %d body %s", status, data)
		}

		// The real thing.
		var queued struct {
			Status string `json:"status"`
			JobID  int64  `json:"job_id"`
		}
		status, data = anon.do(t, http.MethodPost, "/api/hooks/import", payload,
			map[string]string{"X-Webhook-Secret": s.webhookSecret})
		if status != http.StatusOK || json.Unmarshal(data, &queued) != nil || queued.Status != "queued" || queued.JobID == 0 {
			t.Fatalf("Download event: status %d body %s", status, data)
		}

		// Poll until the worker finishes it.
		var final *job
		deadline := time.Now().Add(jobTimeout)
		for final == nil {
			var list struct {
				Jobs []job `json:"jobs"`
			}
			admin.call(t, http.MethodGet, "/api/jobs?limit=50", nil, http.StatusOK, &list)
			for i := range list.Jobs {
				j := &list.Jobs[i]
				if j.ID == queued.JobID && (j.Status == "passed" || j.Status == "failed") {
					final = j
				}
			}
			if final != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %d not finished within %s; jobs: %+v", queued.JobID, jobTimeout, list.Jobs)
			}
			time.Sleep(2 * time.Second)
		}

		if final.Path != containerPath || final.ArrType != "radarr" || final.ArrID != arrID {
			t.Fatalf("job row = %+v, want radarr %d at %s", *final, arrID, containerPath)
		}
		var res jobResult
		if final.Result != "" {
			if err := json.Unmarshal([]byte(final.Result), &res); err != nil {
				t.Fatalf("job result is not JSON: %q: %v", final.Result, err)
			}
		}
		t.Logf("job %d finished %s: error=%q result=%s", final.ID, final.Status, final.Error, final.Result)
		if final.Status == "failed" && final.Error == "" && res.Reason == "" {
			t.Fatalf("job failed without a reason: %+v", *final)
		}
	})

	step(t, "logout", func(t *testing.T) {
		admin.call(t, http.MethodPost, "/api/auth/logout", nil, http.StatusNoContent, nil)
		admin.expectError(t, http.MethodGet, "/api/auth/me", nil, http.StatusUnauthorized)
	})
}
