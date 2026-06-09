package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	arrclient "pelicula-api/internal/clients/arr"
)

// muxArrSvc is an ArrClient whose typed clients all point at one httptest
// server driven by the given mux. Radarr and Sonarr endpoints don't collide
// (/api/v3/movie* vs /api/v3/series*), so one server plays both roles.
type muxArrSvc struct {
	sonarrCli *arrclient.Client
	radarrCli *arrclient.Client
}

func newMuxArrSvc(t *testing.T, mux http.Handler) *muxArrSvc {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &muxArrSvc{
		sonarrCli: arrclient.New(srv.URL, "sonarr-key"),
		radarrCli: arrclient.New(srv.URL, "radarr-key"),
	}
}

func (s *muxArrSvc) Keys() (string, string, string)  { return "sonarr-key", "radarr-key", "" }
func (s *muxArrSvc) SonarrClient() *arrclient.Client { return s.sonarrCli }
func (s *muxArrSvc) RadarrClient() *arrclient.Client { return s.radarrCli }

func applyJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body)) //nolint:errcheck
}

// TestHandleLibraryApply_RegistersMovieAndSeries drives the full apply path
// (strategy "keep", so no filesystem ops): new items are looked up in the
// respective *arr, registered with search disabled, and already-present items
// are skipped without a POST.
func TestHandleLibraryApply_RegistersMovieAndSeries(t *testing.T) {
	var mu sync.Mutex
	var moviePosts, seriesPosts []map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			applyJSON(w, `[{"id":1,"tmdbId":100}]`) // tmdb 100 already in Radarr
			return
		}
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p) //nolint:errcheck
		mu.Lock()
		moviePosts = append(moviePosts, p)
		mu.Unlock()
		applyJSON(w, `{"id":2}`)
	})
	mux.HandleFunc("/api/v3/movie/lookup/tmdb", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `{"title":"Looked Up Movie","tmdbId":200,"year":2021}`)
	})
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			applyJSON(w, `[]`)
			return
		}
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p) //nolint:errcheck
		mu.Lock()
		seriesPosts = append(seriesPosts, p)
		mu.Unlock()
		applyJSON(w, `{"id":3}`)
	})
	mux.HandleFunc("/api/v3/series/lookup", func(w http.ResponseWriter, r *http.Request) {
		if term := r.URL.Query().Get("term"); term != "tvdb:300" {
			t.Errorf("series lookup term = %q, want tvdb:300", term)
		}
		applyJSON(w, `[{"title":"Looked Up Show","tvdbId":300,"year":2019}]`)
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[{"id":4,"name":"HD-1080p"},{"id":9,"name":"Ultra-HD"}]`)
	})

	h := &Handler{Svc: newMuxArrSvc(t, mux)}

	body := `{
		"items": [
			{"type":"movie","tmdbId":100,"title":"Already Here","year":2020,"sourcePath":"/import/here.mkv"},
			{"type":"movie","tmdbId":200,"title":"New Movie","year":2021,"monitored":true,"sourcePath":"/import/new.mkv"},
			{"type":"series","tvdbId":300,"title":"New Show","year":2019,"season":1,"episode":1,"monitored":true,"sourcePath":"/import/show.mkv"}
		],
		"strategy":"keep"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/library/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleLibraryApply(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleLibraryApply: code = %d, body = %s", w.Code, w.Body.String())
	}
	var result LibraryApplyResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Added != 2 || result.Skipped != 1 || result.Failed != 0 {
		t.Errorf("added/skipped/failed = %d/%d/%d, want 2/1/0 (errors: %v)",
			result.Added, result.Skipped, result.Failed, result.Errors)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(moviePosts) != 1 {
		t.Fatalf("movie POSTs = %d, want 1 (existing tmdb 100 must be skipped)", len(moviePosts))
	}
	mp := moviePosts[0]
	if mp["tmdbId"].(float64) != 200 {
		t.Errorf("movie POST tmdbId = %v, want 200", mp["tmdbId"])
	}
	if mp["title"] != "Looked Up Movie" {
		t.Errorf("movie POST title = %v, want lookup result to be forwarded", mp["title"])
	}
	if mp["rootFolderPath"] != "/media/movies" {
		t.Errorf("movie POST rootFolderPath = %v, want /media/movies default", mp["rootFolderPath"])
	}
	if mp["qualityProfileId"].(float64) != 4 {
		t.Errorf("movie POST qualityProfileId = %v, want 4 (lowest profile id)", mp["qualityProfileId"])
	}
	if mp["monitored"] != true {
		t.Errorf("movie POST monitored = %v, want true", mp["monitored"])
	}
	if opts := mp["addOptions"].(map[string]any); opts["searchForMovie"] != false {
		t.Errorf("movie addOptions = %v, want searchForMovie=false", opts)
	}

	if len(seriesPosts) != 1 {
		t.Fatalf("series POSTs = %d, want 1", len(seriesPosts))
	}
	sp := seriesPosts[0]
	if sp["tvdbId"].(float64) != 300 {
		t.Errorf("series POST tvdbId = %v, want 300", sp["tvdbId"])
	}
	if sp["rootFolderPath"] != "/media/tv" {
		t.Errorf("series POST rootFolderPath = %v, want /media/tv default", sp["rootFolderPath"])
	}
	if sp["seasonFolder"] != true {
		t.Errorf("series POST seasonFolder = %v, want true", sp["seasonFolder"])
	}
	if opts := sp["addOptions"].(map[string]any); opts["searchForMissingEpisodes"] != false {
		t.Errorf("series addOptions = %v, want searchForMissingEpisodes=false", opts)
	}
}

// TestHandleLibraryApply_LookupFailureIsReported verifies a failed *arr lookup
// surfaces as a per-item failure with an error, not a dropped item.
func TestHandleLibraryApply_LookupFailureIsReported(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[]`)
	})
	mux.HandleFunc("/api/v3/movie/lookup/tmdb", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tmdb unavailable", http.StatusBadGateway)
	})
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[]`)
	})
	mux.HandleFunc("/api/v3/series/lookup", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[]`) // no results → "series not found"
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[{"id":1,"name":"Any"}]`)
	})

	h := &Handler{Svc: newMuxArrSvc(t, mux)}

	body := `{
		"items": [
			{"type":"movie","tmdbId":200,"title":"Doomed Movie","year":2021,"sourcePath":"/import/doomed.mkv"},
			{"type":"series","tvdbId":300,"title":"Unknown Show","year":2019,"season":1,"episode":1,"sourcePath":"/import/unknown.mkv"}
		],
		"strategy":"keep"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/library/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleLibraryApply(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleLibraryApply: code = %d, body = %s", w.Code, w.Body.String())
	}
	var result LibraryApplyResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Failed != 2 || result.Added != 0 {
		t.Errorf("failed/added = %d/%d, want 2/0 (errors: %v)", result.Failed, result.Added, result.Errors)
	}
	var sawNotFound bool
	for _, e := range result.Errors {
		if strings.Contains(e, "series not found") {
			sawNotFound = true
		}
	}
	if !sawNotFound {
		t.Errorf("errors = %v, want a 'series not found' entry", result.Errors)
	}
}
