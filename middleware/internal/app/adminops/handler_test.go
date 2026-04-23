package adminops_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pelicula-api/internal/app/adminops"
)

// fakeDocker is a minimal DockerClient that records which services were restarted.
type fakeDocker struct {
	restarted []string
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

func (f *fakeDocker) Restart(name string) error {
	f.restarted = append(f.restarted, name)
	return nil
}

func (f *fakeDocker) Logs(name string, tail int, timestamps bool) ([]byte, error) {
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
