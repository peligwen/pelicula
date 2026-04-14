package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newFakeProcula starts a test HTTP server that serves fixed JSON on a path.
func newFakeProcula(t *testing.T, path, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

func TestHandleStorageProxy(t *testing.T) {
	fake := newFakeProcula(t, "/api/procula/storage", `{"volumes":[],"timestamp":"2026-04-06T00:00:00Z"}`)
	defer fake.Close()
	old := proculaURL
	origSvc := services
	proculaURL = fake.URL
	services = NewServiceClients("/config")
	t.Cleanup(func() { proculaURL = old; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/storage", nil)
	w := httptest.NewRecorder()
	handleStorageProxy(w, req)

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
	fake := newFakeProcula(t, "/api/procula/updates", `{"current_version":"dev","update_available":false}`)
	defer fake.Close()
	old := proculaURL
	origSvc := services
	proculaURL = fake.URL
	services = NewServiceClients("/config")
	t.Cleanup(func() { proculaURL = old; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/updates", nil)
	w := httptest.NewRecorder()
	handleUpdatesProxy(w, req)

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
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/storage", nil)
	w := httptest.NewRecorder()
	handleStorageProxy(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleUpdatesProxyMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/updates", nil)
	w := httptest.NewRecorder()
	handleUpdatesProxy(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleStorageProxyBadGateway(t *testing.T) {
	// Point PROCULA_URL at a port with nothing listening.
	origSvc := services
	t.Cleanup(func() { services = origSvc })
	t.Setenv("PROCULA_URL", "http://127.0.0.1:1")
	services = NewServiceClients("/config")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/storage", nil)
	w := httptest.NewRecorder()
	handleStorageProxy(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestHandleUpdatesProxyBadGateway(t *testing.T) {
	origSvc := services
	t.Cleanup(func() { services = origSvc })
	t.Setenv("PROCULA_URL", "http://127.0.0.1:1")
	services = NewServiceClients("/config")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/updates", nil)
	w := httptest.NewRecorder()
	handleUpdatesProxy(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestIsAllowedWebhookPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/downloads/file.mkv", true},
		{"/movies/folder/file.mkv", true},
		{"/tv/show/s01/e01.mkv", true},
		{"/processing/out.mkv", true},
		{"/etc/passwd", false},
		// Exact directory match is allowed
		{"/downloads", true},
		{"/movies", true},
		{"/download/file.mkv", false}, // partial prefix doesn't match
		{"", false},
		{"/var/downloads/file.mkv", false},
		// Path traversal attempts must be blocked
		{"/downloads/../etc/passwd", false},
		{"/movies/../../etc/shadow", false},
		{"/tv/../../../root/.ssh/id_rsa", false},
		{"/processing/../movies/../etc/passwd", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := isAllowedWebhookPath(c.path)
			if got != c.want {
				t.Errorf("isAllowedWebhookPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestNormalizeHookPayload(t *testing.T) {
	t.Run("radarr movie payload", func(t *testing.T) {
		raw := map[string]any{
			"eventType":  "Download",
			"downloadId": "ABC123",
			"movie": map[string]any{
				"title": "Alien",
				"year":  float64(1979),
				"id":    float64(42),
			},
			"movieFile": map[string]any{
				"path": "/movies/Alien (1979)/alien.mkv",
				"size": float64(5_000_000_000),
				"mediaInfo": map[string]any{
					"runTimeSeconds": float64(6960), // 116 minutes
				},
			},
		}

		source, err := normalizeHookPayload(raw)
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
		if source.Path != "/movies/Alien (1979)/alien.mkv" {
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
				"path": "/tv/Breaking Bad/Season 01/s01e01.mkv",
				"size": float64(1_500_000_000),
				"mediaInfo": map[string]any{
					"runTimeSeconds": float64(2700), // 45 minutes
				},
			},
		}

		source, err := normalizeHookPayload(raw)
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
		raw := map[string]any{
			"eventType": "Download",
			"unknown":   map[string]any{},
		}
		_, err := normalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error for missing movie/series key")
		}
	})

	t.Run("missing path returns error", func(t *testing.T) {
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
		_, err := normalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error for missing path")
		}
	})

	t.Run("path outside allowed dirs returns error", func(t *testing.T) {
		raw := map[string]any{
			"movie": map[string]any{
				"title": "Alien",
			},
			"movieFile": map[string]any{
				"path": "/etc/passwd",
			},
		}
		_, err := normalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error for disallowed path")
		}
	})

	t.Run("missing movieFile returns error", func(t *testing.T) {
		raw := map[string]any{
			"movie": map[string]any{
				"title": "Alien",
			},
			// no movieFile — path will be empty → error
		}
		_, err := normalizeHookPayload(raw)
		if err == nil {
			t.Error("expected error when movieFile absent")
		}
	})
}

// ── handleImportHook secret enforcement ───────────────────────────────────────

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
			"path": "/movies/Alien/alien.mkv",
			"size": float64(1_000_000),
		},
	}
	b, _ := json.Marshal(raw)
	return b
}

func TestHandleImportHook_NoSecret_PassesThrough(t *testing.T) {
	// When WEBHOOK_SECRET is unset, the check is skipped (backward compat).
	origSvc := services
	t.Cleanup(func() { services = origSvc })
	t.Setenv("WEBHOOK_SECRET", "")
	services = NewServiceClients("/config")

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleImportHook(w, req)

	// Will fail trying to reach Procula — but must not return 401.
	if w.Code == http.StatusUnauthorized {
		t.Error("expected no 401 when WEBHOOK_SECRET is empty")
	}
}

func TestHandleImportHook_WrongSecret_Returns401(t *testing.T) {
	origSvc := services
	t.Cleanup(func() { services = origSvc })
	t.Setenv("WEBHOOK_SECRET", "correct-secret")
	services = NewServiceClients("/config")

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import?secret=wrong-secret", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleImportHook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for wrong secret", w.Code)
	}
}

func TestHandleImportHook_CorrectSecret_Passes(t *testing.T) {
	origSvc := services
	t.Cleanup(func() { services = origSvc })
	t.Setenv("WEBHOOK_SECRET", "my-secret")
	services = NewServiceClients("/config")

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import?secret=my-secret", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleImportHook(w, req)

	// Procula is unreachable in tests — but must not return 401.
	if w.Code == http.StatusUnauthorized {
		t.Error("expected no 401 for correct secret")
	}
}

func TestHandleImportHook_MissingSecret_Returns401(t *testing.T) {
	origSvc := services
	t.Cleanup(func() { services = origSvc })
	t.Setenv("WEBHOOK_SECRET", "required-secret")
	services = NewServiceClients("/config")

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrPayload()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleImportHook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when secret missing from query", w.Code)
	}
}

// ── handleBrowse symlink escape ───────────────────────────────────────────────

func TestHandleNotificationsProxy_PassesThroughDetailAndJobID(t *testing.T) {
	proculaBody := `[{"id":"notif_1","timestamp":"2026-04-14T10:00:00Z","type":"validation_failed","message":"Validation failed: Dune","detail":"FFmpeg error: codec not supported","job_id":"abc12345"}]`
	fake := newFakeProcula(t, "/api/procula/notifications", proculaBody)
	defer fake.Close()

	old := proculaURL
	origSvc := services
	proculaURL = fake.URL
	services = NewServiceClients("/config")
	t.Cleanup(func() { proculaURL = old; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/notifications", nil)
	w := httptest.NewRecorder()
	handleNotificationsProxy(w, req)

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

func TestHandleBrowse_RejectsOutOfBoundsResolvedPath(t *testing.T) {
	// Create a symlink inside /tmp pointing to /etc, then try to browse via
	// a path that resolves outside the allowed roots. We use a temp dir to
	// simulate the layout since /downloads doesn't exist in tests.
	//
	// The handler checks isAllowedBrowsePath before EvalSymlinks, so a path
	// that is under /downloads but resolves elsewhere would be caught by the
	// second check after EvalSymlinks. We verify the forbidden response.
	//
	// Since we can't create a path under /downloads in tests, we exercise
	// the simpler case: a path not under any root is immediately rejected.
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/browse?path=/etc/passwd", nil)
	w := httptest.NewRecorder()
	handleBrowse(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for path outside allowed roots", w.Code)
	}
}
