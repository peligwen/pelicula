package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pelicula-api/internal/app/library"
)

// ── stub ArrClient ─────────────────────────────────────────────────────────────

type stubArr struct {
	sonarrKey   string
	radarrKey   string
	prowlarrKey string
	doGet       func(baseURL, apiKey, path string) ([]byte, error)
	doPost      func(baseURL, apiKey, path string, payload any) ([]byte, error)
}

func (s *stubArr) Keys() (sonarr, radarr, prowlarr string) {
	return s.sonarrKey, s.radarrKey, s.prowlarrKey
}
func (s *stubArr) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	if s.doGet != nil {
		return s.doGet(baseURL, apiKey, path)
	}
	return nil, fmt.Errorf("stub: unexpected ArrGet baseURL=%q path=%q", baseURL, path)
}
func (s *stubArr) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	if s.doPost != nil {
		return s.doPost(baseURL, apiKey, path, payload)
	}
	return nil, fmt.Errorf("stub: unexpected ArrPost baseURL=%q path=%q", baseURL, path)
}

// httpArrStub builds a stubArr that routes requests to live httptest.Servers.
func httpArrStub(sonarrSrv, radarrSrv, prowlarrSrv *httptest.Server) *stubArr {
	s := &stubArr{sonarrKey: "sk", radarrKey: "rk", prowlarrKey: "pk"}
	s.doGet = func(baseURL, apiKey, path string) ([]byte, error) {
		resp, err := http.Get(baseURL + path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return body, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return body, nil
	}
	s.doPost = func(baseURL, apiKey, path string, payload any) ([]byte, error) {
		data, _ := json.Marshal(payload)
		resp, err := http.Post(baseURL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return body, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return body, nil
	}
	// Route based on which server the baseURL matches.
	base := s.doGet
	s.doGet = func(baseURL, apiKey, path string) ([]byte, error) {
		switch {
		case sonarrSrv != nil && baseURL == sonarrSrv.URL:
		case radarrSrv != nil && baseURL == radarrSrv.URL:
		case prowlarrSrv != nil && baseURL == prowlarrSrv.URL:
		default:
			return nil, fmt.Errorf("stub: no server registered for baseURL=%q", baseURL)
		}
		return base(baseURL, apiKey, path)
	}
	return s
}

// newHandler builds a Handler pointed at live test servers.
func newHandler(sonarrSrv, radarrSrv, prowlarrSrv *httptest.Server, searchMode string) *Handler {
	sonarrURL, radarrURL, prowlarrURL := "", "", ""
	if sonarrSrv != nil {
		sonarrURL = sonarrSrv.URL
	}
	if radarrSrv != nil {
		radarrURL = radarrSrv.URL
	}
	if prowlarrSrv != nil {
		prowlarrURL = prowlarrSrv.URL
	}
	h := New(httpArrStub(sonarrSrv, radarrSrv, prowlarrSrv),
		sonarrURL, radarrURL, prowlarrURL,
		&library.Handler{}, "", searchMode)
	return h
}

// ── log capture ───────────────────────────────────────────────────────────────

type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (l *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
	fmt.Fprintf(os.Stderr, "[%s] %s\n", r.Level, r.Message)
	return nil
}
func (l *logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return l }
func (l *logCapture) WithGroup(_ string) slog.Handler      { return l }

func (l *logCapture) hasWarn(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.records {
		if r.Level >= slog.LevelWarn && strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

func installLogCapture(t *testing.T) *logCapture {
	t.Helper()
	var lc logCapture
	orig := slog.Default()
	slog.SetDefault(slog.New(&lc))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &lc
}

// ── search helper ─────────────────────────────────────────────────────────────

func doSearch(t *testing.T, h *Handler, query, typeFilter string) map[string]any {
	t.Helper()
	u := "/api/pelicula/search?q=" + query
	if typeFilter != "" {
		u += "&type=" + typeFilter
	}
	req := httptest.NewRequest(http.MethodGet, u, nil)
	w := httptest.NewRecorder()
	h.HandleSearch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HandleSearch status = %d; body = %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out
}

func resultsFrom(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["results"].([]any)
	if !ok {
		t.Fatalf("results field missing or wrong type; body = %v", body)
	}
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("results[%d] is not an object", i)
		}
		out[i] = m
	}
	return out
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestHandleSearch_FansOutToBothArrs: q="dune"; one movie from Radarr, one
// series from Sonarr; both appear and the interleave order is series-first.
func TestHandleSearch_FansOutToBothArrs(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"Dune","year":2021,"tmdbId":438631}]`)) //nolint:errcheck
		case "/api/v3/movie":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Dune: Prophecy","year":2024,"tvdbId":422100}]`)) //nolint:errcheck
		case "/api/v3/series":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, radarr, nil, "")
	results := resultsFrom(t, doSearch(t, h, "dune", ""))

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	// Interleave: series always emitted first at each step.
	if results[0]["type"] != "series" {
		t.Errorf("results[0].type = %q, want \"series\"", results[0]["type"])
	}
	if results[1]["type"] != "movie" {
		t.Errorf("results[1].type = %q, want \"movie\"", results[1]["type"])
	}
}

// TestHandleSearch_TypeFilterMovie: only Radarr is queried; Sonarr stub panics
// if hit.
func TestHandleSearch_TypeFilterMovie(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"Dune","year":2021,"tmdbId":438631}]`)) //nolint:errcheck
		case "/api/v3/movie":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Sonarr should NOT be queried when typeFilter=movie (path %s)", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, radarr, nil, "")
	results := resultsFrom(t, doSearch(t, h, "dune", "movie"))

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0]["type"] != "movie" {
		t.Errorf("results[0].type = %q, want \"movie\"", results[0]["type"])
	}
}

// TestHandleSearch_TypeFilterSeries: only Sonarr is queried; Radarr stub panics
// if hit.
func TestHandleSearch_TypeFilterSeries(t *testing.T) {
	t.Parallel()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Dune: Prophecy","year":2024,"tvdbId":422100}]`)) //nolint:errcheck
		case "/api/v3/series":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Radarr should NOT be queried when typeFilter=series (path %s)", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer radarr.Close()

	h := newHandler(sonarr, radarr, nil, "")
	results := resultsFrom(t, doSearch(t, h, "dune", "series"))

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0]["type"] != "series" {
		t.Errorf("results[0].type = %q, want \"series\"", results[0]["type"])
	}
}

// TestHandleSearch_AddedFlagFromExisting: movie with tmdbId=438631 is in the
// Radarr library → results[0].added must be true.
func TestHandleSearch_AddedFlagFromExisting(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"Dune","year":2021,"tmdbId":438631}]`)) //nolint:errcheck
		case "/api/v3/movie":
			// Library already contains this movie.
			w.Write([]byte(`[{"tmdbId":438631,"title":"Dune"}]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series/lookup":
			w.Write([]byte(`[]`)) //nolint:errcheck
		case "/api/v3/series":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, radarr, nil, "")
	results := resultsFrom(t, doSearch(t, h, "dune", "movie"))

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0]["added"] != true {
		t.Errorf("results[0].added = %v, want true", results[0]["added"])
	}
}

// TestHandleSearch_IndexerModeFilters: searchMode="indexer"; Prowlarr returns
// only tmdbId=2 → only that movie survives the filter.
func TestHandleSearch_IndexerModeFilters(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"A","year":2020,"tmdbId":1},{"title":"B","year":2021,"tmdbId":2},{"title":"C","year":2022,"tmdbId":3}]`)) //nolint:errcheck
		case "/api/v3/movie":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series/lookup":
			w.Write([]byte(`[]`)) //nolint:errcheck
		case "/api/v3/series":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only tmdbId=2 has indexer availability.
		w.Write([]byte(`[{"title":"B Release","tmdbId":2}]`)) //nolint:errcheck
	}))
	defer prowlarr.Close()

	h := newHandler(sonarr, radarr, prowlarr, "indexer")
	results := resultsFrom(t, doSearch(t, h, "test", "movie"))

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (only tmdbId=2)", len(results))
	}
	if tmdbID := results[0]["tmdbId"].(float64); int(tmdbID) != 2 {
		t.Errorf("results[0].tmdbId = %v, want 2", tmdbID)
	}
}

// TestHandleSearch_IndexerModeProwlarrFailureDegrades: Prowlarr 5xx → results
// are unfiltered (all 3 movies) and a slog.Warn is emitted.
func TestHandleSearch_IndexerModeProwlarrFailureDegrades(t *testing.T) {
	lc := installLogCapture(t)

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"A","year":2020,"tmdbId":1},{"title":"B","year":2021,"tmdbId":2},{"title":"C","year":2022,"tmdbId":3}]`)) //nolint:errcheck
		case "/api/v3/movie":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series/lookup":
			w.Write([]byte(`[]`)) //nolint:errcheck
		case "/api/v3/series":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer prowlarr.Close()

	h := newHandler(sonarr, radarr, prowlarr, "indexer")
	results := resultsFrom(t, doSearch(t, h, "test", "movie"))

	// All 3 movies should be returned (unfiltered degradation).
	if len(results) != 3 {
		t.Errorf("len(results) = %d, want 3 (unfiltered on Prowlarr error)", len(results))
	}

	if !lc.hasWarn("prowlarr") {
		t.Error("expected a slog.Warn mentioning prowlarr, got none")
	}
}

// TestCachedIndexerSearch_TTL: two calls within TTL → 1 upstream; advance now
// past TTL → 2 upstream calls total.
func TestCachedIndexerSearch_TTL(t *testing.T) {
	var calls atomic.Int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer prowlarr.Close()

	faketime := time.Now()
	h := New(&stubArr{
		sonarrKey:   "sk",
		radarrKey:   "rk",
		prowlarrKey: "pk",
		doGet: func(baseURL, apiKey, path string) ([]byte, error) {
			resp, err := http.Get(baseURL + path)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			return io.ReadAll(resp.Body)
		},
	}, "", "", prowlarr.URL, &library.Handler{}, "", "indexer")
	h.now = func() time.Time { return faketime }

	// First call — cache miss.
	if _, err := h.cachedIndexerSearch("dune"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call within TTL — cache hit, no upstream.
	if _, err := h.cachedIndexerSearch("dune"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("upstream calls = %d after 2 requests within TTL, want 1", calls.Load())
	}

	// Advance past TTL.
	faketime = faketime.Add(indexerSearchTTL + time.Second)

	// Third call — should miss.
	if _, err := h.cachedIndexerSearch("dune"); err != nil {
		t.Fatalf("third call: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("upstream calls = %d after advancing past TTL, want 2", calls.Load())
	}
}

// TestCachedIndexerSearch_LazyEviction: prime A and B, advance past TTL,
// query C → A and B are evicted from the cache map.
func TestCachedIndexerSearch_LazyEviction(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer prowlarr.Close()

	faketime := time.Now()
	h := New(&stubArr{
		sonarrKey:   "sk",
		radarrKey:   "rk",
		prowlarrKey: "pk",
		doGet: func(baseURL, apiKey, path string) ([]byte, error) {
			resp, err := http.Get(baseURL + path)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			return io.ReadAll(resp.Body)
		},
	}, "", "", prowlarr.URL, &library.Handler{}, "", "indexer")
	h.now = func() time.Time { return faketime }

	// Prime A and B.
	h.cachedIndexerSearch("alpha") //nolint:errcheck
	h.cachedIndexerSearch("beta")  //nolint:errcheck

	h.cache.mu.Lock()
	if len(h.cache.entries) != 2 {
		h.cache.mu.Unlock()
		t.Fatalf("expected 2 cache entries after priming, got %d", len(h.cache.entries))
	}
	h.cache.mu.Unlock()

	// Advance past TTL so both entries become stale.
	faketime = faketime.Add(indexerSearchTTL + time.Second)

	// Query a new key C — the lazy eviction loop runs inside cachedIndexerSearch.
	h.cachedIndexerSearch("gamma") //nolint:errcheck

	h.cache.mu.Lock()
	defer h.cache.mu.Unlock()

	if _, ok := h.cache.entries["alpha"]; ok {
		t.Error("entry \"alpha\" should have been evicted")
	}
	if _, ok := h.cache.entries["beta"]; ok {
		t.Error("entry \"beta\" should have been evicted")
	}
	if _, ok := h.cache.entries["gamma"]; !ok {
		t.Error("entry \"gamma\" should be present after fresh fetch")
	}
}

// TestHandleSearchAdd_MovieDelegatesToFulfiller: valid movie body → Radarr
// returns id=99; response is {"status":"added","arr_id":99}.
func TestHandleSearchAdd_MovieDelegatesToFulfiller(t *testing.T) {
	t.Setenv("REQUESTS_RADARR_PROFILE_ID", "1")
	t.Setenv("REQUESTS_RADARR_ROOT", "/media/movies")

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/lookup/tmdb":
			w.Write([]byte(`{"title":"Dune","year":2021,"tmdbId":438631}`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			w.Write([]byte(`{"id":99,"title":"Dune"}`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	h := newHandler(nil, radarr, nil, "")

	body := `{"type":"movie","tmdbId":438631,"title":"Dune","year":2021}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "added" {
		t.Errorf("status = %v, want \"added\"", resp["status"])
	}
	if resp["arr_id"].(float64) != 99 {
		t.Errorf("arr_id = %v, want 99", resp["arr_id"])
	}
}

// TestHandleSearchAdd_RejectsUnknownType: type="album" → 400.
func TestHandleSearchAdd_RejectsUnknownType(t *testing.T) {
	t.Parallel()

	h := New(&stubArr{sonarrKey: "sk", radarrKey: "rk", prowlarrKey: "pk"},
		"", "", "", &library.Handler{}, "", "")

	body := `{"type":"album","tmdbId":1,"title":"OST"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown type", w.Code)
	}
}

// TestHandleSearchAdd_BodySizeLimit: a body larger than 64KB is rejected with
// 400. Pins the MaxBytesReader guard added in P4.
func TestHandleSearchAdd_BodySizeLimit(t *testing.T) {
	t.Parallel()

	h := New(&stubArr{sonarrKey: "sk", radarrKey: "rk", prowlarrKey: "pk"},
		"", "", "", &library.Handler{}, "", "")

	// 200KB body — well over the 64KB limit.
	oversized := strings.Repeat("x", 200<<10)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add",
		strings.NewReader(`{"type":"movie","title":"`+oversized+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (MaxBytesReader should trip)", w.Code)
	}
}

// TestHandleArrMeta_ReturnsProfilesAndRoots: happy path — both arrs return
// quality profiles and root folders.
func TestHandleArrMeta_ReturnsProfilesAndRoots(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":1,"name":"HD-1080p"},{"id":2,"name":"Any"}]`)) //nolint:errcheck
		case "/api/v3/rootfolder":
			w.Write([]byte(`[{"path":"/media/movies"}]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD TV"}]`)) //nolint:errcheck
		case "/api/v3/rootfolder":
			w.Write([]byte(`[{"path":"/media/tv"}]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, radarr, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/search/meta", nil)
	w := httptest.NewRecorder()
	h.HandleArrMeta(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}

	var resp struct {
		Radarr struct {
			QualityProfiles []map[string]any `json:"qualityProfiles"`
			RootFolders     []map[string]any `json:"rootFolders"`
		} `json:"radarr"`
		Sonarr struct {
			QualityProfiles []map[string]any `json:"qualityProfiles"`
			RootFolders     []map[string]any `json:"rootFolders"`
		} `json:"sonarr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Radarr.QualityProfiles) != 2 {
		t.Errorf("radarr quality profiles = %d, want 2", len(resp.Radarr.QualityProfiles))
	}
	if len(resp.Sonarr.QualityProfiles) != 1 {
		t.Errorf("sonarr quality profiles = %d, want 1", len(resp.Sonarr.QualityProfiles))
	}
	if len(resp.Radarr.RootFolders) != 1 {
		t.Errorf("radarr root folders = %d, want 1", len(resp.Radarr.RootFolders))
	}
	if resp.Radarr.RootFolders[0]["path"] != "/media/movies" {
		t.Errorf("radarr root = %v, want /media/movies", resp.Radarr.RootFolders[0]["path"])
	}
}

// TestEnrichSearchResult_PullsRatingFromImdb: IMDb rating is preferred; TMDB
// is the fallback when IMDb is absent.
func TestEnrichSearchResult_PullsRatingFromImdb(t *testing.T) {
	t.Parallel()

	raw := map[string]any{
		"certification": "PG-13",
		"runtime":       float64(155),
		"genres":        []any{"Action", "Adventure"},
		"ratings": map[string]any{
			"imdb": map[string]any{"value": float64(8.1)},
			"tmdb": map[string]any{"value": float64(7.9)},
		},
	}
	var sr SearchResult
	enrichSearchResult(&sr, raw)

	if sr.Rating != 8.1 {
		t.Errorf("rating = %.2f, want 8.1 (IMDb preferred)", sr.Rating)
	}
	if sr.Certification != "PG-13" {
		t.Errorf("certification = %q, want PG-13", sr.Certification)
	}
	if sr.Runtime != 155 {
		t.Errorf("runtime = %d, want 155", sr.Runtime)
	}
	if len(sr.Genres) != 2 {
		t.Errorf("genres = %v, want 2 entries", sr.Genres)
	}
}

// TestEnrichSearchResult_FallsBackToTmdbRating: no IMDb rating → TMDB value used.
func TestEnrichSearchResult_FallsBackToTmdbRating(t *testing.T) {
	t.Parallel()

	raw := map[string]any{
		"ratings": map[string]any{
			"tmdb": map[string]any{"value": float64(7.5)},
		},
	}
	var sr SearchResult
	enrichSearchResult(&sr, raw)

	if sr.Rating != 7.5 {
		t.Errorf("rating = %.2f, want 7.5 (TMDB fallback)", sr.Rating)
	}
}

// TestHandleSearch_EmptyQuery: q="" returns empty results without hitting any
// *arr backend.
func TestHandleSearch_EmptyQuery(t *testing.T) {
	t.Parallel()

	// If either server is hit, the test should fail.
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("backend should not be hit for empty query (path %s)", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	h := newHandler(fail, fail, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/search?q=", nil)
	w := httptest.NewRecorder()
	h.HandleSearch(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	results := resultsFrom(t, func() map[string]any {
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out) //nolint:errcheck
		return out
	}())
	if len(results) != 0 {
		t.Errorf("results = %d, want 0 for empty query", len(results))
	}
}
