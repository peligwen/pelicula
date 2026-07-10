package adminops_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pelicula-api/internal/app/adminops"
)

// fakeDocker is a minimal DockerClient that records which services were
// restarted. Recording is mutex-guarded: HandleStackRestart restarts
// pelicula-api itself on a detached goroutine after responding, so Restart
// can race a test's assertions without it.
type fakeDocker struct {
	mu        sync.Mutex
	restarted []string
	lastTail  int
}

// restartedSnapshot returns a copy of the recorded restart order, safe to
// read while the handler's detached self-restart goroutine may still run.
func (f *fakeDocker) restartedSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restarted...)
}

func (f *fakeDocker) IsAllowed(name string) bool {
	// Allow only services the test cares about; everything else is denied.
	allowed := map[string]bool{
		"nginx": true, "procula": true, "sonarr": true, "radarr": true,
		"prowlarr": true, "qbittorrent": true, "jellyfin": true,
		"bazarr": true, "gluetun": true, "pelicula-api": true,
	}
	return allowed[name]
}

func (f *fakeDocker) Restart(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarted = append(f.restarted, name)
	return nil
}

func (f *fakeDocker) Logs(_ context.Context, name string, tail int, timestamps bool) ([]byte, error) {
	f.lastTail = tail
	return []byte("fake logs"), nil
}

// flushRecorder wraps httptest.ResponseRecorder and records whether Flush was called.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (fr *flushRecorder) Flush() {
	fr.flushed = true
	fr.ResponseRecorder.Flush()
}

// TestHandleStackRestart_FlushCalledAndResponseOK verifies that:
//   - HandleStackRestart calls Flush on the ResponseWriter
//   - The response status is 200
//   - The response body contains {"ok":true}
func TestHandleStackRestart_FlushCalledAndResponseOK(t *testing.T) {
	docker := &fakeDocker{}
	h := adminops.New(docker, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/admin/stack/restart", nil)
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	h.HandleStackRestart(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if !rec.flushed {
		t.Error("Flush was not called on the ResponseWriter")
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	ok, _ := body["ok"].(bool)
	if !ok {
		t.Errorf("response body does not contain ok:true — got: %s", rec.Body.String())
	}
}

// TestHandleStackRestart_GluetunBeforeNamespaceDependents verifies MWA-18's
// real invariant: qbittorrent and prowlarr share gluetun's network namespace,
// so gluetun must be restarted before them (matching HandleVPNRestart's
// order), not after.
func TestHandleStackRestart_GluetunBeforeNamespaceDependents(t *testing.T) {
	docker := &fakeDocker{}
	h := adminops.New(docker, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/admin/stack/restart", nil)
	w := httptest.NewRecorder()
	h.HandleStackRestart(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	order := docker.restartedSnapshot()
	indexOf := func(name string) int {
		for i, s := range order {
			if s == name {
				return i
			}
		}
		t.Fatalf("%q was never restarted; got order %v", name, order)
		return -1
	}

	gluetunIdx := indexOf("gluetun")
	for _, dependent := range []string{"qbittorrent", "prowlarr"} {
		if depIdx := indexOf(dependent); depIdx < gluetunIdx {
			t.Errorf("%s restarted at index %d, before gluetun at index %d; must restart after gluetun to avoid attaching to a torn-down namespace", dependent, depIdx, gluetunIdx)
		}
	}
}

// TestHandleStackRestart_RejectsGET verifies that GET requests are rejected with 405.
func TestHandleStackRestart_RejectsGET(t *testing.T) {
	h := adminops.New(&fakeDocker{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/admin/stack/restart", nil)
	w := httptest.NewRecorder()
	h.HandleStackRestart(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// TestHandleStackRestart_NoFlushFallback verifies that a plain http.ResponseWriter
// (which does not implement http.Flusher) does not panic and still returns 200.
type noFlushWriter struct {
	header http.Header
	body   strings.Builder
	code   int
}

func (n *noFlushWriter) Header() http.Header         { return n.header }
func (n *noFlushWriter) Write(b []byte) (int, error) { return n.body.Write(b) }
func (n *noFlushWriter) WriteHeader(code int)        { n.code = code }

func TestHandleStackRestart_NoFlushFallback(t *testing.T) {
	h := adminops.New(&fakeDocker{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/admin/stack/restart", nil)
	w := &noFlushWriter{header: make(http.Header)}
	// Should not panic even though w does not implement http.Flusher.
	h.HandleStackRestart(w, req)
	// WriteHeader was never called explicitly for 200 (default), so code stays 0.
	// Just verify we got a non-error response body.
	if !strings.Contains(w.body.String(), `"ok":true`) {
		t.Errorf("body does not contain ok:true: %s", w.body.String())
	}
}

// TestRateLimiter_AllowsThenBlocks verifies that the adminops rate limiter
// allows exactly 10 requests per key, then returns 429 on the 11th.
// Threshold is pinned here so a change to ratelimit.go's const limit breaks
// this test loudly rather than silently regressing.
func TestRateLimiter_AllowsThenBlocks(t *testing.T) {
	const limit = 10

	docker := &fakeDocker{}
	h := adminops.New(docker, nil)

	// All requests share the same RemoteAddr so they bucket to the same rate-limit key.
	for i := 1; i <= limit; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/pelicula/admin/logs?svc=nginx", nil)
		req.RemoteAddr = "192.0.2.1:9999"
		w := httptest.NewRecorder()
		h.HandleServiceLogs(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
	}

	// The (limit+1)th request must be rate-limited.
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/admin/logs?svc=nginx", nil)
	req.RemoteAddr = "192.0.2.1:9999"
	w := httptest.NewRecorder()
	h.HandleServiceLogs(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("request %d: status = %d, want 429", limit+1, w.Code)
	}
}

// TestHandleServiceLogs_ClampsTailToMax verifies MWA-11: the documented max
// of 500 (see the HandleServiceLogs doc comment) is actually enforced, not
// just advertised.
func TestHandleServiceLogs_ClampsTailToMax(t *testing.T) {
	docker := &fakeDocker{}
	h := adminops.New(docker, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/admin/logs?svc=nginx&tail=1000000", nil)
	req.RemoteAddr = "192.0.2.2:9999"
	w := httptest.NewRecorder()
	h.HandleServiceLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if docker.lastTail != 500 {
		t.Errorf("tail passed to Docker.Logs = %d, want clamped to 500", docker.lastTail)
	}
}

// TestHandleServiceLogs_TailWithinLimitPassesThrough verifies that a tail
// value under the max is passed through unchanged.
func TestHandleServiceLogs_TailWithinLimitPassesThrough(t *testing.T) {
	docker := &fakeDocker{}
	h := adminops.New(docker, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/admin/logs?svc=nginx&tail=50", nil)
	req.RemoteAddr = "192.0.2.3:9999"
	w := httptest.NewRecorder()
	h.HandleServiceLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if docker.lastTail != 50 {
		t.Errorf("tail passed to Docker.Logs = %d, want 50 (unclamped)", docker.lastTail)
	}
}
