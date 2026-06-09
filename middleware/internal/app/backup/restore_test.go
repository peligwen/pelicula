package backup_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pelicula-api/internal/app/backup"
	arrclient "pelicula-api/internal/clients/arr"
)

// muxArrClient is an ArrClient whose typed clients all point at a single
// httptest server driven by the given mux. Radarr and Sonarr endpoints don't
// collide (/api/v3/movie vs /api/v3/series), so one server plays both roles.
type muxArrClient struct {
	sonarrCli *arrclient.Client
	radarrCli *arrclient.Client
}

func newMuxArrClient(t *testing.T, mux http.Handler) *muxArrClient {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &muxArrClient{
		sonarrCli: arrclient.New(srv.URL, "sonarr-key"),
		radarrCli: arrclient.New(srv.URL, "radarr-key"),
	}
}

func (s *muxArrClient) Keys() (string, string, string) {
	return "sonarr-key", "radarr-key", "prowlarr-key"
}
func (s *muxArrClient) SonarrClient() *arrclient.Client { return s.sonarrCli }
func (s *muxArrClient) RadarrClient() *arrclient.Client { return s.radarrCli }

// importBackup POSTs the given backup through HandleImportBackup and decodes
// the ImportResult.
func importBackup(t *testing.T, h *backup.Handler, bk map[string]any) backup.ImportResult {
	t.Helper()
	body, err := json.Marshal(bk)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/import-backup", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleImportBackup(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleImportBackup: code = %d, body = %s", w.Code, w.Body.String())
	}
	var result backup.ImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal ImportResult: %v", err)
	}
	return result
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body)) //nolint:errcheck
}

// TestHandleImportBackup_RestoresMoviesAndSeries drives the full restore path
// against a stub *arr server: existing items are skipped, new items are added
// with resolved profile/tag IDs and the default root folders, items without a
// tmdb/tvdb ID are reported as failures, and season monitoring is preserved.
func TestHandleImportBackup_RestoresMoviesAndSeries(t *testing.T) {
	var mu sync.Mutex
	var moviePayloads, seriesPayloads []map[string]any
	var createdTagLabels []string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, `[{"id":1,"tmdbId":100,"title":"Already Here"}]`)
			return
		}
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p) //nolint:errcheck
		mu.Lock()
		moviePayloads = append(moviePayloads, p)
		mu.Unlock()
		writeJSON(w, `{"id":2}`)
	})
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, `[]`)
			return
		}
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p) //nolint:errcheck
		mu.Lock()
		seriesPayloads = append(seriesPayloads, p)
		mu.Unlock()
		writeJSON(w, `{"id":3}`)
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `[{"id":7,"name":"HD-1080p"}]`)
	})
	mux.HandleFunc("/api/v3/tag", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, `[]`)
			return
		}
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p) //nolint:errcheck
		label, _ := p["label"].(string)
		mu.Lock()
		createdTagLabels = append(createdTagLabels, label)
		mu.Unlock()
		writeJSON(w, `{"id":31,"label":"`+label+`"}`)
	})

	h := newHandler(newMuxArrClient(t, mux))
	result := importBackup(t, h, map[string]any{
		"version":  1,
		"exported": "2026-01-01T00:00:00Z",
		"movies": []map[string]any{
			{"title": "Already Here", "year": 2020, "tmdbId": 100, "qualityProfile": "HD-1080p"},
			{"title": "New Movie", "year": 2021, "tmdbId": 200, "qualityProfile": "HD-1080p",
				"monitored": true, "tags": []string{"keep"}},
			{"title": "Broken Movie", "year": 2022, "tmdbId": 0},
		},
		"series": []map[string]any{
			{"title": "New Show", "year": 2019, "tvdbId": 300, "qualityProfile": "HD-1080p",
				"monitored": true,
				"seasons": []map[string]any{
					{"seasonNumber": 1, "monitored": true},
					{"seasonNumber": 2, "monitored": false},
				}},
		},
	})

	if result.MoviesAdded != 1 || result.MoviesSkipped != 1 || result.MoviesFailed != 1 {
		t.Errorf("movies added/skipped/failed = %d/%d/%d, want 1/1/1",
			result.MoviesAdded, result.MoviesSkipped, result.MoviesFailed)
	}
	if result.SeriesAdded != 1 || result.SeriesFailed != 0 {
		t.Errorf("series added/failed = %d/%d, want 1/0", result.SeriesAdded, result.SeriesFailed)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "Broken Movie") {
		t.Errorf("errors = %v, want one entry for Broken Movie", result.Errors)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(moviePayloads) != 1 {
		t.Fatalf("AddMovie calls = %d, want 1 (payloads: %v)", len(moviePayloads), moviePayloads)
	}
	mp := moviePayloads[0]
	if got := mp["tmdbId"].(float64); got != 200 {
		t.Errorf("movie payload tmdbId = %v, want 200", got)
	}
	if got := mp["qualityProfileId"].(float64); got != 7 {
		t.Errorf("movie payload qualityProfileId = %v, want 7 (resolved from name)", got)
	}
	if got := mp["rootFolderPath"]; got != "/media/movies" {
		t.Errorf("movie payload rootFolderPath = %v, want /media/movies", got)
	}
	if got := mp["tags"].([]any); len(got) != 1 || got[0].(float64) != 31 {
		t.Errorf("movie payload tags = %v, want [31] (created tag)", got)
	}
	if opts := mp["addOptions"].(map[string]any); opts["searchForMovie"] != false {
		t.Errorf("movie addOptions = %v, want searchForMovie=false", opts)
	}
	if len(createdTagLabels) != 1 || createdTagLabels[0] != "keep" {
		t.Errorf("created tags = %v, want [keep]", createdTagLabels)
	}

	if len(seriesPayloads) != 1 {
		t.Fatalf("AddSeries calls = %d, want 1", len(seriesPayloads))
	}
	sp := seriesPayloads[0]
	if got := sp["tvdbId"].(float64); got != 300 {
		t.Errorf("series payload tvdbId = %v, want 300", got)
	}
	if got := sp["rootFolderPath"]; got != "/media/tv" {
		t.Errorf("series payload rootFolderPath = %v, want /media/tv", got)
	}
	seasons := sp["seasons"].([]any)
	if len(seasons) != 2 {
		t.Fatalf("series payload seasons = %v, want 2 entries", seasons)
	}
	s2 := seasons[1].(map[string]any)
	if s2["seasonNumber"].(float64) != 2 || s2["monitored"] != false {
		t.Errorf("season 2 = %v, want seasonNumber=2 monitored=false", s2)
	}
	if opts := sp["addOptions"].(map[string]any); opts["searchForMissingEpisodes"] != false {
		t.Errorf("series addOptions = %v, want searchForMissingEpisodes=false", opts)
	}
}

// TestHandleImportBackup_UpstreamAddFails verifies that *arr rejections are
// counted as failures with descriptive errors instead of aborting the restore.
func TestHandleImportBackup_UpstreamAddFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, `[]`)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, `[]`)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `[{"id":1,"name":"Any"}]`)
	})
	mux.HandleFunc("/api/v3/tag", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `[]`)
	})

	h := newHandler(newMuxArrClient(t, mux))
	result := importBackup(t, h, map[string]any{
		"version":  1,
		"exported": "2026-01-01T00:00:00Z",
		"movies": []map[string]any{
			{"title": "Doomed Movie", "year": 2021, "tmdbId": 200, "qualityProfile": "Any"},
		},
		"series": []map[string]any{
			{"title": "Doomed Show", "year": 2019, "tvdbId": 300, "qualityProfile": "Any"},
		},
	})

	if result.MoviesFailed != 1 || result.MoviesAdded != 0 {
		t.Errorf("movies failed/added = %d/%d, want 1/0", result.MoviesFailed, result.MoviesAdded)
	}
	if result.SeriesFailed != 1 || result.SeriesAdded != 0 {
		t.Errorf("series failed/added = %d/%d, want 1/0", result.SeriesFailed, result.SeriesAdded)
	}
	if len(result.Errors) != 2 {
		t.Errorf("errors = %v, want 2 entries", result.Errors)
	}
	for _, e := range result.Errors {
		if !strings.Contains(e, "tmdb:200") && !strings.Contains(e, "tvdb:300") {
			t.Errorf("error %q missing tmdb/tvdb identifier", e)
		}
	}
}
