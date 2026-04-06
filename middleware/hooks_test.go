package main

import (
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
	t.Setenv("PROCULA_URL", fake.URL)
	services = NewServiceClients("/config")

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
	t.Setenv("PROCULA_URL", fake.URL)
	services = NewServiceClients("/config")

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
