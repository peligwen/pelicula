package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReadBazarrAPIKey_Success(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "bazarr", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Include a decoy apikey under a different top-level section to
	// confirm the parser scopes lookups to auth:.
	content := "---\naddic7ed:\n  apikey: wrong-key\nauth:\n  apikey: test-key-123\n  password: ''\nother:\n  apikey: also-wrong\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
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
	content := "---\nauth:\n  username: admin\n  password: secret\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := readBazarrAPIKey(dir)
	if err == nil {
		t.Fatal("expected error for missing apikey, got nil")
	}
}

// capturedCall records a single Bazarr subtitle PATCH.
type capturedCall struct {
	method   string
	path     string
	ctype    string
	apikey   string
	form     map[string]string
	language string
}

// captureSrv spins up an httptest server that records every request, replies
// with the given status (one per call, falling back to the last entry), and
// returns a pointer to the captured-calls slice.
func captureSrv(t *testing.T, statuses ...int) (*httptest.Server, *[]capturedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []capturedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		form := make(map[string]string, len(r.PostForm))
		for k, v := range r.PostForm {
			if len(v) > 0 {
				form[k] = v[0]
			}
		}
		calls = append(calls, capturedCall{
			method:   r.Method,
			path:     r.URL.Path,
			ctype:    r.Header.Get("Content-Type"),
			apikey:   r.Header.Get("X-API-KEY"),
			form:     form,
			language: r.PostFormValue("language"),
		})
		status := http.StatusNoContent
		if idx := len(calls) - 1; idx < len(statuses) {
			status = statuses[idx]
		} else if len(statuses) > 0 {
			status = statuses[len(statuses)-1]
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestBazarrSearchSubtitles_Movie_MultiLang(t *testing.T) {
	srv, calls := captureSrv(t)

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
		MissingSubs: []string{"en", "es"},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(*calls))
	}
	for i, c := range *calls {
		if c.method != http.MethodPatch {
			t.Errorf("call %d: method = %q, want PATCH", i, c.method)
		}
		if c.path != "/api/movies/subtitles" {
			t.Errorf("call %d: path = %q, want /api/movies/subtitles", i, c.path)
		}
		if c.ctype != "application/x-www-form-urlencoded" {
			t.Errorf("call %d: content-type = %q", i, c.ctype)
		}
		if c.apikey != "movie-api-key" {
			t.Errorf("call %d: X-API-KEY = %q", i, c.apikey)
		}
		if c.form["radarrid"] != "42" {
			t.Errorf("call %d: radarrid = %q, want 42", i, c.form["radarrid"])
		}
		if c.form["forced"] != "False" {
			t.Errorf("call %d: forced = %q, want False", i, c.form["forced"])
		}
		if c.form["hi"] != "False" {
			t.Errorf("call %d: hi = %q, want False", i, c.form["hi"])
		}
	}
	if (*calls)[0].language != "en" || (*calls)[1].language != "es" {
		t.Errorf("language order = [%q, %q], want [en, es]", (*calls)[0].language, (*calls)[1].language)
	}
}

func TestBazarrSearchSubtitles_Episode_MultiLang(t *testing.T) {
	srv, calls := captureSrv(t)

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
		MissingSubs: []string{"en", "fr"},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(*calls))
	}
	for i, c := range *calls {
		if c.method != http.MethodPatch {
			t.Errorf("call %d: method = %q, want PATCH", i, c.method)
		}
		if c.path != "/api/episodes/subtitles" {
			t.Errorf("call %d: path = %q, want /api/episodes/subtitles", i, c.path)
		}
		if c.apikey != "ep-api-key" {
			t.Errorf("call %d: X-API-KEY = %q", i, c.apikey)
		}
		if c.form["seriesid"] != "10" {
			t.Errorf("call %d: seriesid = %q, want 10", i, c.form["seriesid"])
		}
		if c.form["episodeid"] != "999" {
			t.Errorf("call %d: episodeid = %q, want 999", i, c.form["episodeid"])
		}
	}
	if (*calls)[0].language != "en" || (*calls)[1].language != "fr" {
		t.Errorf("language order = [%q, %q], want [en, fr]", (*calls)[0].language, (*calls)[1].language)
	}
}

func TestBazarrSearchSubtitles_SubLangsFallback(t *testing.T) {
	srv, calls := captureSrv(t)

	orig := bazarrURL
	bazarrURL = srv.URL
	t.Cleanup(func() { bazarrURL = orig })

	dir := t.TempDir()
	writeTestAPIKey(t, dir, "fallback-key")

	t.Setenv("PELICULA_SUB_LANGS", "en, FR")

	// Synthetic resub job: no MissingSubs computed.
	job := &Job{
		ID: "job-fallback",
		Source: JobSource{
			ArrType: "radarr",
			ArrID:   7,
		},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(*calls))
	}
	if (*calls)[0].language != "en" || (*calls)[1].language != "fr" {
		t.Errorf("fallback languages = [%q, %q], want [en, fr]", (*calls)[0].language, (*calls)[1].language)
	}
}

func TestBazarrSearchSubtitles_MissingEpisodeID(t *testing.T) {
	srv, calls := captureSrv(t)

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
		MissingSubs: []string{"en"},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if len(*calls) != 0 {
		t.Errorf("expected no HTTP request when EpisodeID=0, got %d calls", len(*calls))
	}
}

func TestBazarrSearchSubtitles_UnknownArrType(t *testing.T) {
	srv, calls := captureSrv(t)

	orig := bazarrURL
	bazarrURL = srv.URL
	t.Cleanup(func() { bazarrURL = orig })

	dir := t.TempDir()
	writeTestAPIKey(t, dir, "any-key")

	job := &Job{
		ID:          "j1",
		Source:      JobSource{ArrType: "unknown"},
		MissingSubs: []string{"en"},
	}
	bazarrSearchSubtitles(context.Background(), dir, job)

	if len(*calls) != 0 {
		t.Errorf("expected no HTTP call for unknown arr_type, got %d", len(*calls))
	}
}

func TestBazarrSearchSubtitles_ServerError(t *testing.T) {
	// First call returns 500, second returns 204 — confirm the loop
	// keeps going after an error so other languages still get searched.
	srv, calls := captureSrv(t, http.StatusInternalServerError, http.StatusNoContent)

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
		MissingSubs: []string{"en", "es"},
	}

	bazarrSearchSubtitles(context.Background(), dir, job)

	if len(*calls) != 2 {
		t.Errorf("expected both languages attempted, got %d calls", len(*calls))
	}
}

func TestBazarrSearchSubtitlesWithOpts(t *testing.T) {
	var got url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		r.ParseForm()
		got = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	orig := bazarrURL
	bazarrURL = ts.URL
	t.Cleanup(func() { bazarrURL = orig })

	// Write a fake Bazarr config.yaml so readBazarrAPIKey succeeds.
	cfgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgDir, "bazarr/config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "bazarr/config/config.yaml"),
		[]byte("auth:\n  apikey: testkey\n"), 0644); err != nil {
		t.Fatal(err)
	}

	job := &Job{
		Source: JobSource{ArrType: "radarr", ArrID: 42, Title: "T"},
	}
	opts := BazarrSearchOpts{Languages: []string{"es"}, HI: true, Forced: false}
	bazarrSearchSubtitlesWithOpts(context.Background(), cfgDir, job, opts)

	if got.Get("language") != "es" {
		t.Errorf("language = %q, want es", got.Get("language"))
	}
	if got.Get("hi") != "True" {
		t.Errorf("hi = %q, want True", got.Get("hi"))
	}
	if got.Get("forced") != "False" {
		t.Errorf("forced = %q, want False", got.Get("forced"))
	}
}

// writeTestAPIKey writes a minimal config.yaml with the given API key under configDir/bazarr/config/.
func writeTestAPIKey(t *testing.T, configDir, apiKey string) {
	t.Helper()
	cfgDir := filepath.Join(configDir, "bazarr", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\nauth:\n  apikey: " + apiKey + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
