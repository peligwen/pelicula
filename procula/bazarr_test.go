package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadBazarrAPIKey_Success(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "bazarr", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "[auth]\napikey = test-key-123\nother = value\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.ini"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	key, err := readBazarrAPIKey(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "test-key-123" {
		t.Errorf("key = %q, want %q", key, "test-key-123")
	}
}

func TestReadBazarrAPIKey_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := readBazarrAPIKey(dir)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestReadBazarrAPIKey_MissingKey(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "bazarr", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "[auth]\nusername = admin\npassword = secret\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.ini"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := readBazarrAPIKey(dir)
	if err == nil {
		t.Fatal("expected error for missing apikey, got nil")
	}
}

func TestBazarrSearchSubtitles_Movie(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	orig := bazarrURL
	bazarrURL = srv.URL
	t.Cleanup(func() { bazarrURL = orig })

	dir := t.TempDir()
	writeTestAPIKey(t, dir, "movie-api-key")

	job := &Job{
		ID: "job-movie-1",
		Source: JobSource{
			ArrType: "radarr",
			ArrID:   42,
		},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if capturedReq == nil {
		t.Fatal("expected HTTP request, got none")
	}
	if capturedReq.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", capturedReq.Method)
	}
	if capturedReq.URL.Path != "/api/movies/subtitles" {
		t.Errorf("path = %q, want /api/movies/subtitles", capturedReq.URL.Path)
	}
	if got := capturedReq.Header.Get("X-API-KEY"); got != "movie-api-key" {
		t.Errorf("X-API-KEY = %q, want %q", got, "movie-api-key")
	}

	var payload map[string]any
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if radarrID, ok := payload["radarrId"].(float64); !ok || int(radarrID) != 42 {
		t.Errorf("radarrId = %v, want 42", payload["radarrId"])
	}
}

func TestBazarrSearchSubtitles_Episode(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	orig := bazarrURL
	bazarrURL = srv.URL
	t.Cleanup(func() { bazarrURL = orig })

	dir := t.TempDir()
	writeTestAPIKey(t, dir, "ep-api-key")

	job := &Job{
		ID: "job-ep-1",
		Source: JobSource{
			ArrType:   "sonarr",
			ArrID:     10,
			EpisodeID: 999,
		},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if capturedReq == nil {
		t.Fatal("expected HTTP request, got none")
	}
	if capturedReq.URL.Path != "/api/episodes/subtitles" {
		t.Errorf("path = %q, want /api/episodes/subtitles", capturedReq.URL.Path)
	}
	if got := capturedReq.Header.Get("X-API-KEY"); got != "ep-api-key" {
		t.Errorf("X-API-KEY = %q, want %q", got, "ep-api-key")
	}

	var payload map[string]any
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if epID, ok := payload["sonarrEpisodeId"].(float64); !ok || int(epID) != 999 {
		t.Errorf("sonarrEpisodeId = %v, want 999", payload["sonarrEpisodeId"])
	}
}

func TestBazarrSearchSubtitles_MissingEpisodeID(t *testing.T) {
	requestMade := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMade = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	orig := bazarrURL
	bazarrURL = srv.URL
	t.Cleanup(func() { bazarrURL = orig })

	dir := t.TempDir()
	writeTestAPIKey(t, dir, "some-key")

	job := &Job{
		ID: "job-no-ep",
		Source: JobSource{
			ArrType:   "sonarr",
			ArrID:     10,
			EpisodeID: 0, // missing
		},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if requestMade {
		t.Error("expected no HTTP request when EpisodeID=0, but one was made")
	}
}

func TestBazarrSearchSubtitles_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := bazarrURL
	bazarrURL = srv.URL
	t.Cleanup(func() { bazarrURL = orig })

	dir := t.TempDir()
	writeTestAPIKey(t, dir, "key")

	job := &Job{
		ID: "job-err",
		Source: JobSource{
			ArrType: "radarr",
			ArrID:   7,
		},
	}

	// Should not panic — fire-and-forget logs the error and returns.
	bazarrSearchSubtitles(context.Background(), dir, job)
}

// writeTestAPIKey writes a minimal config.ini with the given API key under configDir/bazarr/config/.
func writeTestAPIKey(t *testing.T, configDir, apiKey string) {
	t.Helper()
	cfgDir := filepath.Join(configDir, "bazarr", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "[auth]\napikey = " + apiKey + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.ini"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestBazarrSearchSubtitles_UnknownArrType(t *testing.T) {
	// No HTTP request should be made for an unknown arr type.
	dir := t.TempDir()
	writeTestAPIKey(t, dir, "any-key")

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	old := bazarrURL
	bazarrURL = srv.URL
	t.Cleanup(func() { bazarrURL = old })

	job := &Job{ID: "j1", Source: JobSource{ArrType: "unknown"}}
	bazarrSearchSubtitles(context.Background(), dir, job)
	if called {
		t.Error("expected no HTTP call for unknown arr_type")
	}
}
