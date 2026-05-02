package hooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"pelicula-api/internal/app/hooks"
	proculaclient "pelicula-api/internal/clients/procula"
)

// newFakeProcula starts a test HTTP server that serves fixed JSON on a path.
func newFakeProcula(t *testing.T, path, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	})
	return httptest.NewServer(mux)
}

// newHandler builds a Handler wired to the given fake Procula server.
func newHandler(t *testing.T, fakeURL string) *hooks.Handler {
	t.Helper()
	return &hooks.Handler{
		Procula:    proculaclient.New(fakeURL, ""),
		HTTPClient: &http.Client{},
		ProculaURL: fakeURL,
		SonarrURL:  "",
		RadarrURL:  "",
		GetKeys:    func() (string, string, string) { return "", "", "" },
		// SonarrClient and RadarrClient are nil — tests use empty keys so history
		// fetching is skipped in HandleNotificationsProxy.
	}
}

func TestHandleStorageProxy(t *testing.T) {
	t.Parallel()
	fake := newFakeProcula(t, "/api/procula/storage", `{"volumes":[],"timestamp":"2026-04-06T00:00:00Z"}`)
	defer fake.Close()

	h := newHandler(t, fake.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/storage", nil)
	w := httptest.NewRecorder()
	h.HandleStorageProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := body["volumes"]; !ok {
		t.Error("response missing 'volumes' key")
	}
}

func TestHandleUpdatesProxy(t *testing.T) {
	t.Parallel()
	fake := newFakeProcula(t, "/api/procula/updates", `{"current_version":"dev","update_available":false}`)
	defer fake.Close()

	h := newHandler(t, fake.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/updates", nil)
	w := httptest.NewRecorder()
	h.HandleUpdatesProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["update_available"] != false {
		t.Errorf("update_available = %v, want false", body["update_available"])
	}
}

func TestHandleStorageProxyMethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := newHandler(t, "http://127.0.0.1:1")
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/storage", nil)
	w := httptest.NewRecorder()
	h.HandleStorageProxy(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleUpdatesProxyMethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := newHandler(t, "http://127.0.0.1:1")
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/updates", nil)
	w := httptest.NewRecorder()
	h.HandleUpdatesProxy(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleStorageProxyBadGateway(t *testing.T) {
	t.Parallel()
	h := newHandler(t, "http://127.0.0.1:1")
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/storage", nil)
	w := httptest.NewRecorder()
	h.HandleStorageProxy(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestHandleUpdatesProxyBadGateway(t *testing.T) {
	t.Parallel()
	h := newHandler(t, "http://127.0.0.1:1")
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/updates", nil)
	w := httptest.NewRecorder()
	h.HandleUpdatesProxy(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestIsAllowedWebhookPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"/downloads/file.mkv", true},
		{"/media/movies/folder/file.mkv", true},
		{"/media/tv/show/s01/e01.mkv", true},
		{"/processing/out.mkv", true},
		{"/etc/passwd", false},
		// Exact directory match is allowed
		{"/downloads", true},
		{"/media/movies", true},
		{"/download/file.mkv", false}, // partial prefix doesn't match
		{"", false},
		{"/var/downloads/file.mkv", false},
		// Path traversal attempts must be blocked
		{"/downloads/../etc/passwd", false},
		{"/media/movies/../../etc/shadow", false},
		{"/media/tv/../../../root/.ssh/id_rsa", false},
		{"/processing/../../etc/passwd", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			got := hooks.IsAllowedWebhookPath(c.path)
			if got != c.want {
				t.Errorf("IsAllowedWebhookPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestNormalizeHookPayload(t *testing.T) {
	t.Parallel()

	t.Run("radarr movie payload", func(t *testing.T) {
		t.Parallel()
		raw := map[string]any{
			"eventType":  "Download",
			"downloadId": "ABC123",
			"movie": map[string]any{
				"title": "Alien",
				"year":  float64(1979),
				"id":    float64(42),
			},
			"movieFile": map[string]any{
				"path": "/media/movies/Alien (1979)/alien.mkv",
				"size": float64(5_000_000_000),
				"mediaInfo": map[string]any{
					"runTimeSeconds": float64(6960), // 116 minutes
				},
			},
		}

		source, err := hooks.NormalizeHookPayload(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if source.ArrType != "radarr" {
			t.Errorf("ArrType = %q, want %q", source.ArrType, "radarr")
		}
		if source.Type != "movie" {
			t.Errorf("Type = %q, want %q", source.Type, "movie")
		}
		if source.Title != "Alien" {
			t.Errorf("Title = %q, want %q", source.Title, "Alien")
		}
		if source.Year != 1979 {
			t.Errorf("Year = %d, want 1979", source.Year)
		}
		if source.ArrID != 42 {
			t.Errorf("ArrID = %d, want 42", source.ArrID)
		}
		if source.Path != "/media/movies/Alien (1979)/alien.mkv" {
			t.Errorf("Path = %q", source.Path)
		}
		if source.Size != 5_000_000_000 {
			t.Errorf("Size = %d, want 5000000000", source.Size)
		}
		if source.ExpectedRuntimeMinutes != 116 {
			t.Errorf("ExpectedRuntimeMinutes = %d, want 116", source.ExpectedRuntimeMinutes)
		}
		if source.DownloadHash != "ABC123" {
			t.Errorf("DownloadHash = %q, want ABC123", source.DownloadHash)
		}
	})

	t.Run("sonarr episode payload", func(t *testing.T) {
		t.Parallel()
		raw := map[string]any{
			"eventType": "Download",
			"series": map[string]any{
				"title": "Breaking Bad",
				"year":  float64(2008),
				"id":    float64(7),
			},
			"episodes": []any{
				map[string]any{
					"id":            float64(42),
					"seasonNumber":  float64(1),
					"episodeNumber": float64(3),
				},
			},
			"episodeFile": map[string]any{
				"path": "/media/tv/Breaking Bad/Season 01/s01e01.mkv",
				"size": float64(1_500_000_000),
				"mediaInfo": map[string]any{
					"runTimeSeconds": float64(2700), // 45 minutes
				},
			},
		}

		source, err := hooks.NormalizeHookPayload(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if source.ArrType != "sonarr" {
			t.Errorf("ArrType = %q, want %q", source.ArrType, "sonarr")
		}
		if source.Type != "episode" {
			t.Errorf("Type = %q, want %q", source.Type, "episode")
		}
		if source.Title != "Breaking Bad" {
			t.Errorf("Title = %q, want %q", source.Title, "Breaking Bad")
		}
		if source.ExpectedRuntimeMinutes != 45 {
			t.Errorf("ExpectedRuntimeMinutes = %d, want 45", source.ExpectedRuntimeMinutes)
		}
		if source.EpisodeID != 42 {
			t.Errorf("EpisodeID = %d, want 42", source.EpisodeID)
		}
		if source.SeasonNumber != 1 {
			t.Errorf("SeasonNumber = %d, want 1", source.SeasonNumber)
		}
		if source.EpisodeNumber != 3 {
			t.Errorf("EpisodeNumber = %d, want 3", source.EpisodeNumber)
		}
	})

	t.Run("missing movie and series key returns error", func(t *testing.T) {
		t.Parallel()
		raw := map[string]any{
			"eventType": "Download",
			"unknown":   map[string]any{},
		}
		_, err := hooks.NormalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error for missing movie/series key")
		}
	})

	t.Run("missing path returns error", func(t *testing.T) {
		t.Parallel()
		raw := map[string]any{
			"movie": map[string]any{
				"title": "Alien",
				"year":  float64(1979),
			},
			"movieFile": map[string]any{
				// no path
				"size": float64(1000000),
			},
		}
		_, err := hooks.NormalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error for missing path")
		}
	})

	t.Run("path outside allowed dirs returns error", func(t *testing.T) {
		t.Parallel()
		raw := map[string]any{
			"movie": map[string]any{
				"title": "Alien",
			},
			"movieFile": map[string]any{
				"path": "/etc/passwd",
			},
		}
		_, err := hooks.NormalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error for disallowed path")
		}
	})

	t.Run("missing movieFile returns error", func(t *testing.T) {
		t.Parallel()
		raw := map[string]any{
			"movie": map[string]any{
				"title": "Alien",
			},
			// no movieFile — path will be empty → error
		}
		_, err := hooks.NormalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error when movieFile absent")
		}
	})
}

// ── HandleImportHook secret enforcement ──────────────────────────────────────

func newRadarrPayload() []byte {
	raw := map[string]any{
		"eventType":  "Download",
		"downloadId": "hash123",
		"movie": map[string]any{
			"title": "Alien",
			"year":  float64(1979),
			"id":    float64(1),
		},
		"movieFile": map[string]any{
			"path": "/media/movies/Alien/alien.mkv",
			"size": float64(1_000_000),
		},
	}
	b, _ := json.Marshal(raw)
	return b
}

func newImportHandler(fakeURL string, webhookSecret string) *hooks.Handler {
	return &hooks.Handler{
		Procula:       proculaclient.New(fakeURL, ""),
		HTTPClient:    &http.Client{},
		ProculaURL:    fakeURL,
		WebhookSecret: webhookSecret,
		GetKeys:       func() (string, string, string) { return "", "", "" },
	}
}

func TestHandleImportHook_NoSecret_PassesThrough(t *testing.T) {
	t.Parallel()
	h := newImportHandler("http://127.0.0.1:1", "") // Procula unreachable — intentional

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleImportHook(w, req)

	// Will fail trying to reach Procula — but must not return 401.
	if w.Code == http.StatusUnauthorized {
		t.Error("expected no 401 when WebhookSecret is empty")
	}
}

func TestHandleImportHook_WrongSecret_Returns401(t *testing.T) {
	t.Parallel()
	h := newImportHandler("http://127.0.0.1:1", "correct-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Secret", "wrong-secret")
	w := httptest.NewRecorder()
	h.HandleImportHook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for wrong secret", w.Code)
	}
}

func TestHandleImportHook_CorrectSecret_Passes(t *testing.T) {
	t.Parallel()
	h := newImportHandler("http://127.0.0.1:1", "my-secret") // Procula unreachable — intentional

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Secret", "my-secret")
	w := httptest.NewRecorder()
	h.HandleImportHook(w, req)

	// Procula is unreachable in tests — but must not return 401.
	if w.Code == http.StatusUnauthorized {
		t.Error("expected no 401 for correct secret")
	}
}

func TestHandleImportHook_MissingSecret_Returns401(t *testing.T) {
	t.Parallel()
	h := newImportHandler("http://127.0.0.1:1", "required-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	// No X-Webhook-Secret header set — should 401.
	w := httptest.NewRecorder()
	h.HandleImportHook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when X-Webhook-Secret header is missing", w.Code)
	}
}

// TestProxyProcula_PropagatesCtxCancel verifies that proxyProcula threads
// r.Context() through to the upstream request so that a cancelled context
// aborts the in-flight call promptly.
func TestProxyProcula_PropagatesCtxCancel(t *testing.T) {
	t.Parallel()

	// Fake Procula that blocks until its own request context is cancelled.
	blockingProcula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client disconnects
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer blockingProcula.Close()

	h := newHandler(t, blockingProcula.URL)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/storage", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.HandleStorageProxy(w, req)
	}()

	// Cancel after a short delay — handler must unblock well within 100ms.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// good — returned promptly
	case <-time.After(200 * time.Millisecond):
		t.Fatal("HandleStorageProxy did not return after context cancel — ctx not wired through")
	}
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 after ctx cancel", w.Code)
	}
}

func TestHandleNotificationsProxy_PassesThroughDetailAndJobID(t *testing.T) {
	t.Parallel()
	proculaBody := `[{"id":"notif_1","timestamp":"2026-04-14T10:00:00Z","type":"validation_failed","message":"Validation failed: Dune","detail":"FFmpeg error: codec not supported","job_id":"abc12345"}]`
	fake := newFakeProcula(t, "/api/procula/notifications", proculaBody)
	defer fake.Close()

	h := &hooks.Handler{
		Procula:    proculaclient.New(fake.URL, ""),
		HTTPClient: &http.Client{},
		ProculaURL: fake.URL,
		SonarrURL:  "",
		RadarrURL:  "",
		GetKeys:    func() (string, string, string) { return "", "", "" },
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/notifications", nil)
	w := httptest.NewRecorder()
	h.HandleNotificationsProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var events []struct {
		ID     string `json:"id"`
		Detail string `json:"detail"`
		JobID  string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Detail != "FFmpeg error: codec not supported" {
		t.Errorf("Detail = %q, want %q", events[0].Detail, "FFmpeg error: codec not supported")
	}
	if events[0].JobID != "abc12345" {
		t.Errorf("JobID = %q, want %q", events[0].JobID, "abc12345")
	}
}

// TestProxyProculaMutate_MaxBytesReader verifies that proxyProculaMutate rejects
// bodies exceeding 1 MiB with 400 and never forwards the request to Procula.
func TestProxyProculaMutate_MaxBytesReader(t *testing.T) {
	t.Parallel()

	var hit int32
	fakeProcula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeProcula.Close()

	h := newHandler(t, fakeProcula.URL)

	// Body just over the 1 MiB cap.
	oversized := make([]byte, (1<<20)+1)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/storage/scan", bytes.NewReader(oversized))
	req.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	h.HandleStorageScanProxy(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized body", w.Code)
	}
	if n := atomic.LoadInt32(&hit); n != 0 {
		t.Errorf("fake Procula received %d request(s), want 0 — oversized body should not reach upstream", n)
	}
}

// TestProxyProculaMutate_AcceptsBodyUnderLimit verifies that bodies within the
// 1 MiB cap are forwarded to Procula successfully.
func TestProxyProculaMutate_AcceptsBodyUnderLimit(t *testing.T) {
	t.Parallel()

	var hit int32
	fakeProcula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer fakeProcula.Close()

	h := newHandler(t, fakeProcula.URL)

	// Body well within the 1 MiB cap.
	smallBody := []byte(`{"enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/storage/scan", bytes.NewReader(smallBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleStorageScanProxy(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for body within limit", w.Code)
	}
	if n := atomic.LoadInt32(&hit); n != 1 {
		t.Errorf("fake Procula received %d request(s), want 1", n)
	}
}
