package downloads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	arr "pelicula-api/internal/clients/arr"
	qbt "pelicula-api/internal/clients/qbt"
)

// stubSvc implements Svc for tests using real typed clients backed by
// httptest servers. Each test constructs its own servers with canned responses.
type stubSvc struct {
	sonarrSrv *httptest.Server
	radarrSrv *httptest.Server
	qbtSrv    *httptest.Server
	keysRet   [3]string // sonarr, radarr, prowlarr
}

func (s *stubSvc) Keys() (sonarr, radarr, prowlarr string) {
	return s.keysRet[0], s.keysRet[1], s.keysRet[2]
}

func (s *stubSvc) SonarrClient() *arr.Client {
	return arr.New(s.sonarrSrv.URL, s.keysRet[0])
}

func (s *stubSvc) RadarrClient() *arr.Client {
	return arr.New(s.radarrSrv.URL, s.keysRet[1])
}

func (s *stubSvc) QbtClient() *qbt.Client {
	return qbt.New(s.qbtSrv.URL)
}

// newStubSvc creates a stubSvc with three httptest servers using the provided
// mux handlers. Pass nil for any you don't need (falls back to a 404 mux).
func newStubSvc(t *testing.T, sonarrMux, radarrMux, qbtMux http.Handler) *stubSvc {
	t.Helper()
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if sonarrMux == nil {
		sonarrMux = notFound
	}
	if radarrMux == nil {
		radarrMux = notFound
	}
	if qbtMux == nil {
		qbtMux = notFound
	}
	s := &stubSvc{
		sonarrSrv: httptest.NewServer(sonarrMux),
		radarrSrv: httptest.NewServer(radarrMux),
		qbtSrv:    httptest.NewServer(qbtMux),
	}
	t.Cleanup(func() {
		s.sonarrSrv.Close()
		s.radarrSrv.Close()
		s.qbtSrv.Close()
	})
	return s
}

// downstreamDown returns an error that simulates a refused connection.
var downstreamDown = fmt.Errorf("connection refused")

// --- HandleDownloads ---

func TestHandleDownloads_Happy(t *testing.T) {
	torrentsJSON := `[
		{"hash":"abc123","name":"Movie.mkv","progress":0.5,"dlspeed":1024,"upspeed":512,"eta":300,"state":"downloading","size":1073741824,"category":"radarr"},
		{"hash":"def456","name":"Show.S01.mkv","progress":1.0,"dlspeed":0,"upspeed":0,"eta":0,"state":"queuedDL","size":536870912,"category":"sonarr"}
	]`
	statsJSON := `{"dl_info_speed":2048,"up_info_speed":1024}`

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(torrentsJSON))
	})
	qbtMux.HandleFunc("/api/v2/transfer/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(statsJSON))
	})

	svc := newStubSvc(t, nil, nil, qbtMux)
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads", nil)
	w := httptest.NewRecorder()
	h.HandleDownloads(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloads happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloads happy: unmarshal: %v", err)
	}
	if len(resp.Torrents) != 2 {
		t.Errorf("HandleDownloads happy: want 2 torrents, got %d", len(resp.Torrents))
	}
	if resp.Stats.Active != 1 {
		t.Errorf("HandleDownloads happy: want Active=1, got %d", resp.Stats.Active)
	}
	if resp.Stats.Queued != 1 {
		t.Errorf("HandleDownloads happy: want Queued=1, got %d", resp.Stats.Queued)
	}
	if resp.Stats.DLSpeed != 2048 {
		t.Errorf("HandleDownloads happy: want DLSpeed=2048, got %d", resp.Stats.DLSpeed)
	}
}

func TestHandleDownloads_Upstream_Down(t *testing.T) {
	// qbt server returns 503 to simulate unavailability.
	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})

	svc := newStubSvc(t, nil, nil, qbtMux)
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads", nil)
	w := httptest.NewRecorder()
	h.HandleDownloads(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("HandleDownloads down: code = %d, want 502", w.Code)
	}
}

// --- HandleDownloadStats ---

func TestHandleDownloadStats_Happy(t *testing.T) {
	statsJSON := `{"dl_info_speed":4096,"up_info_speed":512}`

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/transfer/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(statsJSON))
	})

	svc := newStubSvc(t, nil, nil, qbtMux)
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads/stats", nil)
	w := httptest.NewRecorder()
	h.HandleDownloadStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloadStats happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var stats DownloadStats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("HandleDownloadStats happy: unmarshal: %v", err)
	}
	if stats.DLSpeed != 4096 {
		t.Errorf("HandleDownloadStats happy: DLSpeed = %d, want 4096", stats.DLSpeed)
	}
	if stats.UPSpeed != 512 {
		t.Errorf("HandleDownloadStats happy: UPSpeed = %d, want 512", stats.UPSpeed)
	}
}

func TestHandleDownloadStats_Upstream_Down(t *testing.T) {
	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/transfer/info", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})

	svc := newStubSvc(t, nil, nil, qbtMux)
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads/stats", nil)
	w := httptest.NewRecorder()
	h.HandleDownloadStats(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("HandleDownloadStats down: code = %d, want 502", w.Code)
	}
}

// --- HandleDownloadPause ---

func TestHandleDownloadPause_Happy(t *testing.T) {
	var stopCalled atomic.Bool

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/stop", func(w http.ResponseWriter, r *http.Request) {
		stopCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	svc := newStubSvc(t, nil, nil, qbtMux)
	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","paused":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloadPause happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloadPause happy: unmarshal: %v", err)
	}
	if resp["status"] != "paused" {
		t.Errorf("HandleDownloadPause happy: status = %q, want paused", resp["status"])
	}
	if !stopCalled.Load() {
		t.Error("HandleDownloadPause happy: expected qbt stop endpoint to be called")
	}
}

func TestHandleDownloadPause_Resume(t *testing.T) {
	var startCalled atomic.Bool

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/start", func(w http.ResponseWriter, r *http.Request) {
		startCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	svc := newStubSvc(t, nil, nil, qbtMux)
	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","paused":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloadPause resume: code = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloadPause resume: unmarshal: %v", err)
	}
	if resp["status"] != "resumed" {
		t.Errorf("HandleDownloadPause resume: status = %q, want resumed", resp["status"])
	}
	if !startCalled.Load() {
		t.Error("HandleDownloadPause resume: expected qbt start endpoint to be called")
	}
}

func TestHandleDownloadPause_Upstream_Down(t *testing.T) {
	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/stop", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})

	svc := newStubSvc(t, nil, nil, qbtMux)
	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","paused":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadPause(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("HandleDownloadPause down: code = %d, want 502", w.Code)
	}
}

// --- HandleDownloadCancel ---

func TestHandleDownloadCancel_Happy(t *testing.T) {
	queueRecords := []map[string]any{
		{"downloadId": "abc123abc123", "id": float64(42), "movieId": float64(7)},
	}
	queueJSON, _ := json.Marshal(map[string]any{
		"totalRecords": 1,
		"records":      queueRecords,
	})
	movieJSON, _ := json.Marshal(map[string]any{"id": 7, "title": "Test Movie", "monitored": true})

	var putCalled atomic.Bool
	var deleteCalled atomic.Bool

	radarrMux := http.NewServeMux()
	// Queue endpoint (used twice: unmonitor pass + remove-from-queue pass)
	radarrMux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(queueJSON)
	})
	// Movie fetch for unmonitor
	radarrMux.HandleFunc("/api/v3/movie/7", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write(movieJSON)
		case http.MethodPut:
			putCalled.Store(true)
			w.WriteHeader(http.StatusAccepted)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// Queue item delete
	radarrMux.HandleFunc("/api/v3/queue/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalled.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	svc := newStubSvc(t, nil, radarrMux, qbtMux)
	svc.keysRet = [3]string{"sonarr-key", "radarr-key", "prowlarr-key"}

	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","category":"radarr","blocklist":false,"reason":"not interested"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadCancel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloadCancel happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloadCancel happy: unmarshal: %v", err)
	}
	if resp["status"] != "removed" {
		t.Errorf("HandleDownloadCancel happy: status = %q, want removed", resp["status"])
	}
	if !putCalled.Load() {
		t.Error("HandleDownloadCancel happy: expected PUT (unmonitor movie) to be called, but it was not")
	}
	if !deleteCalled.Load() {
		t.Error("HandleDownloadCancel happy: expected DELETE (remove from queue) to be called, but it was not")
	}
}

// TestHandleDownloadCancel_UnknownCategory verifies that an unrecognised category
// is rejected with 422 UnprocessableEntity before any arr or qbt operations fire.
func TestHandleDownloadCancel_UnknownCategory(t *testing.T) {
	svc := newStubSvc(t, nil, nil, nil)
	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","category":"unknown"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadCancel(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("HandleDownloadCancel unknown category: code = %d, want 422", w.Code)
	}
}

// TestHandleDownloadCancel_QbtPostErr verifies that a qBittorrent delete failure
// is logged but does not change the 200 response (best-effort cleanup).
func TestHandleDownloadCancel_QbtPostErr(t *testing.T) {
	emptyQueueJSON, _ := json.Marshal(map[string]any{"totalRecords": 0, "records": []any{}})

	radarrMux := http.NewServeMux()
	radarrMux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(emptyQueueJSON)
	})

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})

	svc := newStubSvc(t, nil, radarrMux, qbtMux)
	svc.keysRet = [3]string{"sonarr-key", "radarr-key", ""}
	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","category":"radarr","blocklist":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadCancel(w, req)

	// Handler still returns 200 even when qbt delete fails
	if w.Code != http.StatusOK {
		t.Errorf("HandleDownloadCancel qbt err: code = %d, want 200", w.Code)
	}
}

// TestHandleDownloadCancel_QbtDown verifies that when qBittorrent is unreachable
// the handler still returns 200 (best-effort cleanup — arr side effects may still
// have fired, but qbt delete failure is non-fatal).
func TestHandleDownloadCancel_QbtDown(t *testing.T) {
	emptyQueueJSON, _ := json.Marshal(map[string]any{"totalRecords": 0, "records": []any{}})

	radarrMux := http.NewServeMux()
	radarrMux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(emptyQueueJSON)
	})

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})

	svc := newStubSvc(t, nil, radarrMux, qbtMux)
	svc.keysRet = [3]string{"sonarr-key", "radarr-key", ""}
	h := &Handler{Svc: svc}

	body := `{"hash":"deadbeef1234","category":"radarr","blocklist":false,"reason":"qbt offline"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadCancel(w, req)

	// qbt delete failure is logged but must not change the response — caller gets 200.
	if w.Code != http.StatusOK {
		t.Errorf("HandleDownloadCancel qbt down: code = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloadCancel qbt down: unmarshal: %v", err)
	}
	if resp["status"] != "removed" {
		t.Errorf("HandleDownloadCancel qbt down: status = %q, want removed", resp["status"])
	}
}

// TestHandleDownloads_MethodNotAllowed verifies that non-GET requests are rejected.
func TestHandleDownloads_MethodNotAllowed(t *testing.T) {
	svc := newStubSvc(t, nil, nil, nil)
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads", bytes.NewReader(nil))
	w := httptest.NewRecorder()
	h.HandleDownloads(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("HandleDownloads method not allowed: code = %d, want 405", w.Code)
	}
}

// TestHandleDownloadPause_BadRequest verifies missing hash returns 400.
func TestHandleDownloadPause_BadRequest(t *testing.T) {
	svc := newStubSvc(t, nil, nil, nil)
	h := &Handler{Svc: svc}

	body := `{"paused":true}` // no hash
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadPause(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleDownloadPause bad request: code = %d, want 400", w.Code)
	}
}

// Compile-time check that stubSvc implements Svc.
var _ Svc = (*stubSvc)(nil)
