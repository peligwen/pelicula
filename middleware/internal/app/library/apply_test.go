package library

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestHandleLibraryApply_InterleavesMoveAndRegister is the MWA-6 regression
// test: with strategy "import", each item's file must be moved immediately
// before that item's own *arr registration is attempted — not as a batch
// move for the whole request followed by a batch of registrations. It
// verifies both halves of that guarantee: (1) a registration failure on one
// item does not stop later items from being processed, and (2) the failed
// item's error names the destination its file was already moved to, so an
// admin can find and manually register it instead of it silently rotting in
// the library root.
func TestHandleLibraryApply_InterleavesMoveAndRegister(t *testing.T) {
	base := t.TempDir()
	srcRoot := filepath.Join(base, "downloads")
	dstRoot := filepath.Join(base, "movies")
	for _, d := range []string{srcRoot, dstRoot} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	srcs := make([]string, 3)
	for i, name := range []string{"one.mkv", "two.mkv", "three.mkv"} {
		srcs[i] = filepath.Join(srcRoot, name)
		if err := os.WriteFile(srcs[i], []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	dsts := []string{
		filepath.Join(dstRoot, "one.mkv"),
		filepath.Join(dstRoot, "two.mkv"),
		filepath.Join(dstRoot, "three.mkv"),
	}

	var mu sync.Mutex
	var registeredTmdbIDs []int

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			applyJSON(w, `[]`)
			return
		}
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p) //nolint:errcheck
		id := int(p["tmdbId"].(float64))
		if id == 202 {
			// Item 2 (tmdbId 202) always fails registration.
			http.Error(w, "radarr unavailable", http.StatusBadGateway)
			return
		}
		mu.Lock()
		registeredTmdbIDs = append(registeredTmdbIDs, id)
		mu.Unlock()
		applyJSON(w, `{"id":1}`)
	})
	mux.HandleFunc("/api/v3/movie/lookup/tmdb", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("tmdbId")
		applyJSON(w, fmt.Sprintf(`{"title":"Movie","tmdbId":%s,"year":2020}`, id))
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[{"id":1,"name":"Any"}]`)
	})
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[]`)
	})

	h := &Handler{
		Svc:                  newMuxArrSvc(t, mux),
		applyAllowedSrcRoots: []string{srcRoot},
		applyAllowedDstRoots: []string{dstRoot},
	}

	body := fmt.Sprintf(`{
		"items": [
			{"type":"movie","tmdbId":201,"title":"Movie One","year":2020,"sourcePath":%q,"destPath":%q},
			{"type":"movie","tmdbId":202,"title":"Movie Two","year":2020,"sourcePath":%q,"destPath":%q},
			{"type":"movie","tmdbId":203,"title":"Movie Three","year":2020,"sourcePath":%q,"destPath":%q}
		],
		"strategy":"import"
	}`, srcs[0], dsts[0], srcs[1], dsts[1], srcs[2], dsts[2])

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
	if result.Added != 2 || result.Failed != 1 {
		t.Fatalf("added/failed = %d/%d, want 2/1 (errors: %v)", result.Added, result.Failed, result.Errors)
	}

	// Item 3 (tmdbId 203) must still have been registered even though item 2
	// failed — proves the loop didn't abort or skip subsequent items.
	mu.Lock()
	registered := append([]int(nil), registeredTmdbIDs...)
	mu.Unlock()
	var sawOne, sawThree bool
	for _, id := range registered {
		if id == 201 {
			sawOne = true
		}
		if id == 203 {
			sawThree = true
		}
	}
	if !sawOne || !sawThree {
		t.Errorf("registeredTmdbIDs = %v, want 201 and 203 both present", registered)
	}

	// Item 2's error must say the file was already moved and name the
	// destination — not just the raw *arr error.
	var errFor2 string
	for _, e := range result.Errors {
		if strings.Contains(e, "Movie Two") {
			errFor2 = e
		}
	}
	if errFor2 == "" {
		t.Fatalf("no error recorded for Movie Two; errors = %v", result.Errors)
	}
	if !strings.Contains(errFor2, "already moved") {
		t.Errorf("error for Movie Two = %q, want it to say the file was already moved", errFor2)
	}
	if !strings.Contains(errFor2, dsts[1]) {
		t.Errorf("error for Movie Two = %q, want it to name destination %q", errFor2, dsts[1])
	}

	// The file for item 2 really did move to its destination on disk despite
	// the registration failure — the crash-safety property MWA-6 requires:
	// the admin can find and manually register it.
	if _, err := os.Stat(dsts[1]); err != nil {
		t.Errorf("dst for Movie Two should exist on disk after move: %v", err)
	}
	if _, err := os.Stat(srcs[1]); !os.IsNotExist(err) {
		t.Errorf("src for Movie Two should be gone after move")
	}
}

// TestHandleLibraryApply_FSOpFailureSkipsRegistration verifies that when the
// filesystem operation itself fails (not the *arr call), the item is
// reported as Failed and *arr registration is never attempted for it — since
// the file never reached its destination there is nothing to register.
// Later items must still be processed.
func TestHandleLibraryApply_FSOpFailureSkipsRegistration(t *testing.T) {
	base := t.TempDir()
	srcRoot := filepath.Join(base, "downloads")
	dstRoot := filepath.Join(base, "movies")
	for _, d := range []string{srcRoot, dstRoot} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	srcs := make([]string, 2)
	for i, name := range []string{"blocked.mkv", "ok.mkv"} {
		srcs[i] = filepath.Join(srcRoot, name)
		if err := os.WriteFile(srcs[i], []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Item 1's destination parent is unreadable, so MkdirAll/Lstat on it fails.
	blockedDir := filepath.Join(dstRoot, "blocked")
	if err := os.Mkdir(blockedDir, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(blockedDir, 0755) }) //nolint:errcheck
	dstBlocked := filepath.Join(blockedDir, "sub", "blocked.mkv")
	dstOK := filepath.Join(dstRoot, "ok.mkv")

	var registeredCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			applyJSON(w, `[]`)
			return
		}
		registeredCount++
		applyJSON(w, `{"id":1}`)
	})
	mux.HandleFunc("/api/v3/movie/lookup/tmdb", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("tmdbId")
		applyJSON(w, fmt.Sprintf(`{"title":"Movie","tmdbId":%s,"year":2020}`, id))
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		applyJSON(w, `[{"id":1,"name":"Any"}]`)
	})

	h := &Handler{
		Svc:                  newMuxArrSvc(t, mux),
		applyAllowedSrcRoots: []string{srcRoot},
		applyAllowedDstRoots: []string{dstRoot},
	}

	body := fmt.Sprintf(`{
		"items": [
			{"type":"movie","tmdbId":401,"title":"Blocked Movie","year":2020,"sourcePath":%q,"destPath":%q},
			{"type":"movie","tmdbId":402,"title":"OK Movie","year":2020,"sourcePath":%q,"destPath":%q}
		],
		"strategy":"import"
	}`, srcs[0], dstBlocked, srcs[1], dstOK)

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
	if result.Added != 1 || result.Failed != 1 {
		t.Fatalf("added/failed = %d/%d, want 1/1 (errors: %v)", result.Added, result.Failed, result.Errors)
	}
	if registeredCount != 1 {
		t.Errorf("registeredCount = %d, want 1 (blocked item must not reach *arr)", registeredCount)
	}
	var sawFSFailure bool
	for _, e := range result.Errors {
		if strings.Contains(e, "Blocked Movie") && strings.Contains(e, "filesystem operation failed") {
			sawFSFailure = true
		}
	}
	if !sawFSFailure {
		t.Errorf("errors = %v, want a filesystem-operation-failed entry for Blocked Movie", result.Errors)
	}
}
