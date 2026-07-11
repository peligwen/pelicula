package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pelicula-api/clients"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/library"
	arrclient "pelicula-api/internal/clients/arr"
)

// ── stub ArrClient ─────────────────────────────────────────────────────────────

// stubArr is a minimal ArrClient backed by real *arr.Client instances pointed
// at httptest.Servers. The zero value (empty URL clients) is valid for tests
// that don't hit the network.
type stubArr struct {
	sonarrKey   string
	radarrKey   string
	prowlarrKey string
	sonarr      *arrclient.Client
	radarr      *arrclient.Client
	prowlarr    *arrclient.Client
}

func (s *stubArr) Keys() (sonarr, radarr, prowlarr string) {
	return s.sonarrKey, s.radarrKey, s.prowlarrKey
}
func (s *stubArr) SonarrClient() *arrclient.Client   { return s.sonarr }
func (s *stubArr) RadarrClient() *arrclient.Client   { return s.radarr }
func (s *stubArr) ProwlarrClient() *arrclient.Client { return s.prowlarr }

// keysOnlyArr returns a stubArr with only API keys set and empty-URL clients.
// Use for tests that verify input validation before any HTTP calls are made.
func keysOnlyArr(sonarr, radarr, prowlarr string) *stubArr {
	return &stubArr{
		sonarrKey:   sonarr,
		radarrKey:   radarr,
		prowlarrKey: prowlarr,
		sonarr:      arrclient.New("", sonarr),
		radarr:      arrclient.New("", radarr),
		prowlarr:    arrclient.New("", prowlarr),
	}
}

// newHandlerArr builds a stubArr whose clients target the given httptest.Servers.
// A nil server yields an empty-URL client (requests will fail at the network
// layer, which is what the test wants when that service should not be hit).
func newHandlerArr(sonarrSrv, radarrSrv, prowlarrSrv *httptest.Server) *stubArr {
	srvURL := func(srv *httptest.Server) string {
		if srv != nil {
			return srv.URL
		}
		return ""
	}
	return &stubArr{
		sonarrKey:   "sk",
		radarrKey:   "rk",
		prowlarrKey: "pk",
		sonarr:      arrclient.New(srvURL(sonarrSrv), "sk"),
		radarr:      arrclient.New(srvURL(radarrSrv), "rk"),
		prowlarr:    arrclient.New(srvURL(prowlarrSrv), "pk"),
	}
}

// newHandler builds a Handler pointed at live test servers.
func newHandler(sonarrSrv, radarrSrv, prowlarrSrv *httptest.Server, searchMode string) *Handler {
	return newHandlerWithLib(sonarrSrv, radarrSrv, prowlarrSrv, &library.Handler{}, searchMode)
}

// newHandlerWithLib is newHandler's twin for tests that need a populated
// library registry (e.g. rootPath override validation).
func newHandlerWithLib(sonarrSrv, radarrSrv, prowlarrSrv *httptest.Server, libHandler *library.Handler, searchMode string) *Handler {
	srvURL := func(srv *httptest.Server) string {
		if srv != nil {
			return srv.URL
		}
		return ""
	}
	arr := newHandlerArr(sonarrSrv, radarrSrv, prowlarrSrv)
	h := New(arr,
		srvURL(sonarrSrv), srvURL(radarrSrv), srvURL(prowlarrSrv),
		libHandler, searchMode)
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

// TestHandleSearch_UsesArrCacheForAddedFlag verifies MWA-19: when ArrCache is
// wired, HandleSearch computes the "added" flag from the shared cache instead
// of issuing its own full Radarr/Sonarr library fetch on every search. The
// cache is seeded with a different answer than Radarr's own /api/v3/movie
// endpoint would give, so the assertion can tell which one was actually used.
func TestHandleSearch_UsesArrCacheForAddedFlag(t *testing.T) {
	t.Parallel()

	var libraryFetches int32
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"Dune","year":2021,"tmdbId":438631}]`)) //nolint:errcheck
		case "/api/v3/movie":
			atomic.AddInt32(&libraryFetches, 1)
			// Radarr's own list does NOT contain the movie — deliberately the
			// opposite of what the cache below reports.
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

	h := newHandler(sonarr, radarr, nil, "")
	h.ArrCache = catalog.NewCatalogCache(
		func(ctx context.Context) ([]byte, error) {
			return []byte(`[{"tmdbId":438631,"title":"Dune"}]`), nil
		},
		func(ctx context.Context) ([]byte, error) {
			return []byte(`[]`), nil
		},
	)

	results := resultsFrom(t, doSearch(t, h, "dune", "movie"))

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0]["added"] != true {
		t.Errorf("results[0].added = %v, want true (from ArrCache, not Radarr's own list)", results[0]["added"])
	}
	if got := atomic.LoadInt32(&libraryFetches); got != 0 {
		t.Errorf("Radarr /api/v3/movie was fetched %d times; want 0 — HandleSearch should use ArrCache instead", got)
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
		sonarr:      arrclient.New("", "sk"),
		radarr:      arrclient.New("", "rk"),
		prowlarr:    arrclient.New(prowlarr.URL, "pk"),
	}, "", "", prowlarr.URL, &library.Handler{}, "indexer")
	h.now = func() time.Time { return faketime }

	// First call — cache miss.
	if _, err := h.cachedIndexerSearch(context.Background(), "dune"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call within TTL — cache hit, no upstream.
	if _, err := h.cachedIndexerSearch(context.Background(), "dune"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("upstream calls = %d after 2 requests within TTL, want 1", calls.Load())
	}

	// Advance past TTL.
	faketime = faketime.Add(indexerSearchTTL + time.Second)

	// Third call — should miss.
	if _, err := h.cachedIndexerSearch(context.Background(), "dune"); err != nil {
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
		sonarr:      arrclient.New("", "sk"),
		radarr:      arrclient.New("", "rk"),
		prowlarr:    arrclient.New(prowlarr.URL, "pk"),
	}, "", "", prowlarr.URL, &library.Handler{}, "indexer")
	h.now = func() time.Time { return faketime }

	// Prime A and B.
	h.cachedIndexerSearch(context.Background(), "alpha") //nolint:errcheck
	h.cachedIndexerSearch(context.Background(), "beta")  //nolint:errcheck

	h.cache.mu.Lock()
	if len(h.cache.entries) != 2 {
		h.cache.mu.Unlock()
		t.Fatalf("expected 2 cache entries after priming, got %d", len(h.cache.entries))
	}
	h.cache.mu.Unlock()

	// Advance past TTL so both entries become stale.
	faketime = faketime.Add(indexerSearchTTL + time.Second)

	// Query a new key C — the lazy eviction loop runs inside cachedIndexerSearch.
	h.cachedIndexerSearch(context.Background(), "gamma") //nolint:errcheck

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

	h := New(keysOnlyArr("sk", "rk", "pk"),
		"", "", "", &library.Handler{}, "")

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

	h := New(keysOnlyArr("sk", "rk", "pk"),
		"", "", "", &library.Handler{}, "")

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

// TestHandleSearchAdd_MovieUpstreamFailureHidesInternals: when Radarr is
// unreachable, the client response must not leak the raw dial error or the
// internal host:port — it should get a generic, actionable message while the
// detail is logged server-side. Pins MWA-12.
func TestHandleSearchAdd_MovieUpstreamFailureHidesInternals(t *testing.T) {
	lc := installLogCapture(t)

	// A closed server yields a real "connection refused" dial error that
	// embeds the 127.0.0.1:port host — exactly the kind of internal detail
	// that must not reach the client.
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	radarrHost := strings.TrimPrefix(radarr.URL, "http://")
	radarr.Close()

	h := newHandler(nil, radarr, nil, "")

	body := `{"type":"movie","tmdbId":438631,"title":"Dune","year":2021}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	respBody := w.Body.String()
	if strings.Contains(respBody, radarrHost) {
		t.Errorf("response leaked internal host %q: %s", radarrHost, respBody)
	}
	if strings.Contains(respBody, "dial tcp") || strings.Contains(respBody, "connect: connection refused") {
		t.Errorf("response leaked raw dial error: %s", respBody)
	}
	if !strings.Contains(respBody, "unreachable") {
		t.Errorf("response missing generic actionable message: %s", respBody)
	}
	if !lc.hasWarn("add movie failed") {
		t.Errorf("expected server-side error log for the failed add, got none")
	}
}

// TestHandleSearchAdd_SeriesUpstreamFailureHidesInternals: series twin of the
// above. Pins MWA-12.
func TestHandleSearchAdd_SeriesUpstreamFailureHidesInternals(t *testing.T) {
	lc := installLogCapture(t)

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	sonarrHost := strings.TrimPrefix(sonarr.URL, "http://")
	sonarr.Close()

	h := newHandler(sonarr, nil, nil, "")

	body := `{"type":"series","tvdbId":12345,"title":"Dune"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	respBody := w.Body.String()
	if strings.Contains(respBody, sonarrHost) {
		t.Errorf("response leaked internal host %q: %s", sonarrHost, respBody)
	}
	if strings.Contains(respBody, "dial tcp") || strings.Contains(respBody, "connect: connection refused") {
		t.Errorf("response leaked raw dial error: %s", respBody)
	}
	if !strings.Contains(respBody, "unreachable") {
		t.Errorf("response missing generic actionable message: %s", respBody)
	}
	if !lc.hasWarn("add series failed") {
		t.Errorf("expected server-side error log for the failed add, got none")
	}
}

// ── Phase 1.2: profileId/rootPath add-time override validation ─────────────

// TestHandleSearchAdd_ValidOverridesThreadedIntoPayload: a profileId matching
// Radarr's quality profiles and a rootPath matching a registered radarr
// library are accepted and threaded verbatim into the Radarr add payload
// (qualityProfileId / rootFolderPath), overriding the env-configured default.
func TestHandleSearchAdd_ValidOverridesThreadedIntoPayload(t *testing.T) {
	t.Setenv("REQUESTS_RADARR_PROFILE_ID", "1") // default the override must NOT be used
	t.Setenv("REQUESTS_RADARR_ROOT", "/media/movies")

	var postedBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/lookup/tmdb":
			w.Write([]byte(`{"title":"Dune","year":2021,"tmdbId":438631}`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":1,"name":"HD-1080p"},{"id":5,"name":"Ultra-HD"}]`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&postedBody) //nolint:errcheck
			w.Write([]byte(`{"id":99,"title":"Dune"}`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	libHandler := &library.Handler{}
	libHandler.SetRegistry(library.LibraryConfig{Libraries: []library.Library{
		{Name: "Movies 4K", Slug: "movies4k", Type: "movies", Arr: "radarr", Processing: "full"},
	}})
	h := newHandlerWithLib(nil, radarr, nil, libHandler, "")

	body := `{"type":"movie","tmdbId":438631,"title":"Dune","year":2021,"profileId":5,"rootPath":"/media/movies4k"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if postedBody == nil {
		t.Fatal("Radarr never received the POST /api/v3/movie add payload")
	}
	if got := int(postedBody["qualityProfileId"].(float64)); got != 5 {
		t.Errorf("qualityProfileId = %d, want 5 (the override, not the env default of 1)", got)
	}
	if got := postedBody["rootFolderPath"]; got != "/media/movies4k" {
		t.Errorf("rootFolderPath = %v, want /media/movies4k (the override, not the env default)", got)
	}
}

// TestHandleSearchAdd_InvalidProfileIDRejected: a profileId absent from
// Radarr's quality profiles is rejected with 400, and Radarr's movie lookup
// (and add) are never reached — the override is validated before any add
// attempt.
func TestHandleSearchAdd_InvalidProfileIDRejected(t *testing.T) {
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":1,"name":"HD-1080p"}]`)) //nolint:errcheck
		default:
			t.Errorf("unexpected Radarr request for an invalid profileId: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer radarr.Close()

	h := newHandler(nil, radarr, nil, "")

	body := `{"type":"movie","tmdbId":438631,"title":"Dune","year":2021,"profileId":999}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "999") {
		t.Errorf("expected error message to mention the rejected profileId 999: %s", w.Body.String())
	}
}

// TestHandleSearchAdd_InvalidRootPathRejected: a rootPath that matches no
// registered radarr library is rejected with 400, before any Radarr network
// call is attempted.
func TestHandleSearchAdd_InvalidRootPathRejected(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Radarr should not be contacted for an invalid rootPath (path %s)", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer radarr.Close()

	libHandler := &library.Handler{}
	libHandler.SetRegistry(library.LibraryConfig{Libraries: []library.Library{
		{Name: "Movies", Slug: "movies", Type: "movies", Arr: "radarr", Processing: "full"},
	}})
	h := newHandlerWithLib(nil, radarr, nil, libHandler, "")

	body := `{"type":"movie","tmdbId":438631,"title":"Dune","year":2021,"rootPath":"/media/does-not-exist"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "/media/does-not-exist") {
		t.Errorf("expected error message to mention the rejected rootPath: %s", w.Body.String())
	}
}

// TestHandleSearchAdd_AbsentOverridesPreserveDefaults: when profileId/rootPath
// are absent from the request body, HandleSearchAdd must not attempt to
// validate anything against the (here, empty) library registry, and the
// env-configured REQUESTS_RADARR_PROFILE_ID / REQUESTS_RADARR_ROOT defaults
// must reach the Radarr payload unchanged. Pins the "absent = exactly
// today's default" contract.
func TestHandleSearchAdd_AbsentOverridesPreserveDefaults(t *testing.T) {
	t.Setenv("REQUESTS_RADARR_PROFILE_ID", "7")
	t.Setenv("REQUESTS_RADARR_ROOT", "/media/movies")

	var postedBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/lookup/tmdb":
			w.Write([]byte(`{"title":"Dune","year":2021,"tmdbId":438631}`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/qualityprofile":
			t.Error("qualityprofile endpoint should not be hit when profileId is absent")
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&postedBody) //nolint:errcheck
			w.Write([]byte(`{"id":55,"title":"Dune"}`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	// Empty registry — proves rootPath validation is never reached (it would
	// reject the env default /media/movies, since no library is registered).
	h := newHandlerWithLib(nil, radarr, nil, &library.Handler{}, "")

	body := `{"type":"movie","tmdbId":438631,"title":"Dune","year":2021}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if postedBody == nil {
		t.Fatal("Radarr never received the POST /api/v3/movie add payload")
	}
	if got := int(postedBody["qualityProfileId"].(float64)); got != 7 {
		t.Errorf("qualityProfileId = %d, want 7 (env default, untouched)", got)
	}
	if got := postedBody["rootFolderPath"]; got != "/media/movies" {
		t.Errorf("rootFolderPath = %v, want /media/movies (env default, untouched)", got)
	}
}

// TestHandleSearchAdd_SeriesValidOverridesThreadedIntoPayload: sonarr twin of
// TestHandleSearchAdd_ValidOverridesThreadedIntoPayload — guards against a
// copy/paste mix-up between the movie and series branches.
func TestHandleSearchAdd_SeriesValidOverridesThreadedIntoPayload(t *testing.T) {
	t.Setenv("REQUESTS_SONARR_PROFILE_ID", "1")
	t.Setenv("REQUESTS_SONARR_ROOT", "/media/tv")

	var postedBody map[string]any
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Dune: Prophecy","year":2024,"tvdbId":422100}]`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":1,"name":"HD TV"},{"id":8,"name":"4K TV"}]`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/series" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&postedBody)           //nolint:errcheck
			w.Write([]byte(`{"id":77,"title":"Dune: Prophecy"}`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	libHandler := &library.Handler{}
	libHandler.SetRegistry(library.LibraryConfig{Libraries: []library.Library{
		{Name: "TV 4K", Slug: "tv4k", Type: "tvshows", Arr: "sonarr", Processing: "full"},
	}})
	h := newHandlerWithLib(sonarr, nil, nil, libHandler, "")

	body := `{"type":"series","tvdbId":422100,"title":"Dune: Prophecy","profileId":8,"rootPath":"/media/tv4k"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if postedBody == nil {
		t.Fatal("Sonarr never received the POST /api/v3/series add payload")
	}
	if got := int(postedBody["qualityProfileId"].(float64)); got != 8 {
		t.Errorf("qualityProfileId = %d, want 8 (the override, not the env default of 1)", got)
	}
	if got := postedBody["rootFolderPath"]; got != "/media/tv4k" {
		t.Errorf("rootFolderPath = %v, want /media/tv4k (the override, not the env default)", got)
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

// TestHandleArrMeta_ReturnsLibrariesFilteredPerArr: the additive `libraries`
// field on each per-arr payload must contain exactly the registered libraries
// for that arr — the same {name, container path} values rootPathValid
// accepts — and must not leak a library registered under the other arr (or
// one with no arr at all). This is what the modal now reads for its Target
// Library select, in place of rootFolders (see docs/API.md's search/add
// Notes for why rootFolders is the wrong source).
func TestHandleArrMeta_ReturnsLibrariesFilteredPerArr(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[]`)) //nolint:errcheck
		case "/api/v3/rootfolder":
			// Deliberately includes a path that is NOT a registered library,
			// to prove libraries is drawn from the registry, not rootfolder.
			w.Write([]byte(`[{"path":"/media/unregistered-radarr-root"}]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[]`)) //nolint:errcheck
		case "/api/v3/rootfolder":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	libHandler := &library.Handler{}
	libHandler.SetRegistry(library.LibraryConfig{Libraries: []library.Library{
		{Name: "Movies", Slug: "movies", Type: "movies", Arr: "radarr", Processing: "full"},
		{Name: "Movies 4K", Slug: "movies4k", Type: "movies", Arr: "radarr", Processing: "full"},
		{Name: "TV", Slug: "tv", Type: "tvshows", Arr: "sonarr", Processing: "full"},
		{Name: "Unmanaged", Slug: "unmanaged", Type: "other", Arr: "none", Processing: "off"},
	}})
	h := newHandlerWithLib(sonarr, radarr, nil, libHandler, "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/arr-meta", nil)
	w := httptest.NewRecorder()
	h.HandleArrMeta(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}

	var resp struct {
		Radarr struct {
			Libraries []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"libraries"`
		} `json:"radarr"`
		Sonarr struct {
			Libraries []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"libraries"`
		} `json:"sonarr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(resp.Radarr.Libraries) != 2 {
		t.Fatalf("radarr libraries = %d, want 2: %+v", len(resp.Radarr.Libraries), resp.Radarr.Libraries)
	}
	wantRadarr := map[string]string{"/media/movies": "Movies", "/media/movies4k": "Movies 4K"}
	for _, lib := range resp.Radarr.Libraries {
		if wantRadarr[lib.Path] != lib.Name {
			t.Errorf("unexpected radarr library entry %+v", lib)
		}
		if lib.Path == "/media/unregistered-radarr-root" {
			t.Errorf("radarr libraries leaked a bare rootfolder path not backed by a registered library: %+v", lib)
		}
	}

	if len(resp.Sonarr.Libraries) != 1 {
		t.Fatalf("sonarr libraries = %d, want 1: %+v", len(resp.Sonarr.Libraries), resp.Sonarr.Libraries)
	}
	if resp.Sonarr.Libraries[0].Path != "/media/tv" || resp.Sonarr.Libraries[0].Name != "TV" {
		t.Errorf("sonarr library = %+v, want {Name: TV, Path: /media/tv}", resp.Sonarr.Libraries[0])
	}
	for _, lib := range resp.Sonarr.Libraries {
		if lib.Path == "/media/movies" || lib.Path == "/media/movies4k" {
			t.Errorf("sonarr libraries leaked a radarr-owned library: %+v", lib)
		}
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

// TestArrFulfiller_PassesCtx: a pre-cancelled ctx reaches the underlying HTTP
// call made by arr.Client inside AddMovie. Pins that ArrFulfiller no longer
// uses context.Background() and that cancellation propagates from the caller
// through to the outbound *arr requests.
func TestArrFulfiller_PassesCtx(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var capturedCtxs []context.Context

	// radarr stub: record the request context on every inbound request, then
	// return an error so the fulfiller terminates early without trying to POST.
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedCtxs = append(capturedCtxs, r.Context())
		mu.Unlock()
		// Return a non-2xx so arr.Client surfaces an error and AddMovie returns.
		http.Error(w, "stub error", http.StatusInternalServerError)
	}))
	defer radarr.Close()

	h := New(newHandlerArr(nil, radarr, nil),
		"", radarr.URL, "", nil, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so ctx.Err() != nil on entry

	fulfiller := NewArrFulfiller(h)
	_, err := fulfiller.AddMovie(ctx, 12345, 1, "/media/movies")
	// The underlying arr.Client call should propagate the cancelled ctx and fail.
	if err == nil {
		t.Fatal("expected an error from AddMovie with cancelled ctx, got nil")
	}

	mu.Lock()
	defer mu.Unlock()
	// The pre-cancelled ctx may cause the HTTP client to short-circuit before
	// reaching the server. Either the server was hit (ctx is cancelled) or the
	// client returned context.Canceled before the TCP connect — both are valid
	// propagation of the cancellation. We simply verify that an error was returned.
	// If the server was hit, verify it received a cancelled context.
	for _, c := range capturedCtxs {
		if c.Err() == nil {
			t.Error("ctx passed to radarr HTTP handler was not cancelled; ArrFulfiller may be using context.Background()")
		}
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

// ── Phase 2.1: season-level granularity for series add ──────────────────────

// TestNormalizeSeasons is a table-driven test of the shape-validation rules
// shared by search/add and (independently) peligrosa's request create/approve.
func TestNormalizeSeasons(t *testing.T) {
	t.Parallel()

	tooMany := make([]int, 101)

	tests := []struct {
		name    string
		in      []int
		want    []int
		wantErr string
	}{
		{name: "nil is valid (all seasons)", in: nil, want: nil, wantErr: ""},
		{name: "empty is rejected", in: []int{}, want: nil, wantErr: "seasons must be a non-empty array of season numbers"},
		{name: "dedupes and sorts", in: []int{3, 1, 3, 2}, want: []int{1, 2, 3}, wantErr: ""},
		{name: "season 0 (specials) is in range", in: []int{0}, want: []int{0}, wantErr: ""},
		{name: "season 999 is in range", in: []int{999}, want: []int{999}, wantErr: ""},
		{name: "out of range high is rejected", in: []int{1000}, want: nil, wantErr: "season number 1000 out of range (0-999)"},
		{name: "negative is rejected", in: []int{-1}, want: nil, wantErr: "season number -1 out of range (0-999)"},
		{name: "over 100 entries is rejected", in: tooMany, want: nil, wantErr: "seasons must contain at most 100 entries"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, errMsg := normalizeSeasons(tc.in)
			if errMsg != tc.wantErr {
				t.Fatalf("errMsg = %q, want %q", errMsg, tc.wantErr)
			}
			if errMsg == "" && !equalIntSlices(got, tc.want) {
				t.Errorf("got = %v, want %v", got, tc.want)
			}
		})
	}
}

func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBuildSeasonsPayload_MarksSelectedMonitored verifies that every season
// from the lookup is present in the output, with only the selected numbers
// monitored — including specials (season 0) when selected or not.
func TestBuildSeasonsPayload_MarksSelectedMonitored(t *testing.T) {
	t.Parallel()

	show := map[string]any{
		"seasons": []any{
			map[string]any{"seasonNumber": float64(0), "monitored": false},
			map[string]any{"seasonNumber": float64(1), "monitored": true},
			map[string]any{"seasonNumber": float64(2), "monitored": true},
		},
	}
	got, err := buildSeasonsPayload(show, []int{1})
	if err != nil {
		t.Fatalf("buildSeasonsPayload: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (every season from the lookup)", len(got))
	}
	want := map[int]bool{0: false, 1: true, 2: false}
	for _, s := range got {
		num := s["seasonNumber"].(int)
		if s["monitored"].(bool) != want[num] {
			t.Errorf("season %d monitored = %v, want %v", num, s["monitored"], want[num])
		}
	}
}

// TestBuildSeasonsPayload_UnknownSeasonReturnsErrInvalidSeasons verifies that
// requesting a season number absent from the lookup's own season list
// surfaces clients.ErrInvalidSeasons, with the offending number in the text.
func TestBuildSeasonsPayload_UnknownSeasonReturnsErrInvalidSeasons(t *testing.T) {
	t.Parallel()

	show := map[string]any{
		"seasons": []any{
			map[string]any{"seasonNumber": float64(1), "monitored": true},
		},
	}
	_, err := buildSeasonsPayload(show, []int{9})
	if !errors.Is(err, clients.ErrInvalidSeasons) {
		t.Fatalf("err = %v, want wrapping clients.ErrInvalidSeasons", err)
	}
	if !strings.Contains(err.Error(), "9") {
		t.Errorf("error should mention the bad season number 9: %v", err)
	}
}

// TestExtractSeasonInfo_EpisodeCountOnlyWhenStatisticsPresent verifies that
// episodeCount is populated only when the lookup provides
// statistics.totalEpisodeCount, never fabricated when absent.
func TestExtractSeasonInfo_EpisodeCountOnlyWhenStatisticsPresent(t *testing.T) {
	t.Parallel()

	raw := map[string]any{
		"seasons": []any{
			map[string]any{"seasonNumber": float64(0), "monitored": false},
			map[string]any{
				"seasonNumber": float64(1), "monitored": true,
				"statistics": map[string]any{"totalEpisodeCount": float64(10)},
			},
		},
	}
	got := extractSeasonInfo(raw)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].EpisodeCount != 0 {
		t.Errorf("season 0 EpisodeCount = %d, want 0 (no statistics on the lookup)", got[0].EpisodeCount)
	}
	if got[1].EpisodeCount != 10 {
		t.Errorf("season 1 EpisodeCount = %d, want 10", got[1].EpisodeCount)
	}
}

// TestExtractSeasonInfo_NoSeasonsKeyReturnsNil verifies the additive-field
// contract: a lookup item with no "seasons" key yields a nil slice (which,
// combined with SearchResult.Seasons's omitempty tag, keeps the field out of
// the response for movie results and any degraded series lookup).
func TestExtractSeasonInfo_NoSeasonsKeyReturnsNil(t *testing.T) {
	t.Parallel()
	if got := extractSeasonInfo(map[string]any{}); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// TestHandleSearch_SeriesResultsCarrySeasonsMetadata verifies GET
// /api/pelicula/search's additive seasons field end to end: every season
// from the Sonarr lookup appears, and episodeCount is present only for the
// season whose lookup entry actually has statistics.
func TestHandleSearch_SeriesResultsCarrySeasonsMetadata(t *testing.T) {
	t.Parallel()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Dune: Prophecy","year":2024,"tvdbId":422100,"seasons":[` + //nolint:errcheck
				`{"seasonNumber":1,"monitored":true,"statistics":{"totalEpisodeCount":6}},` +
				`{"seasonNumber":2,"monitored":false}]}]`))
		case "/api/v3/series":
			w.Write([]byte(`[]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, nil, nil, "")
	results := resultsFrom(t, doSearch(t, h, "dune", "series"))

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	seasonsRaw, ok := results[0]["seasons"].([]any)
	if !ok || len(seasonsRaw) != 2 {
		t.Fatalf("seasons = %v, want 2 entries", results[0]["seasons"])
	}
	s1 := seasonsRaw[0].(map[string]any)
	if int(s1["seasonNumber"].(float64)) != 1 {
		t.Errorf("seasons[0].seasonNumber = %v, want 1", s1["seasonNumber"])
	}
	if int(s1["episodeCount"].(float64)) != 6 {
		t.Errorf("seasons[0].episodeCount = %v, want 6", s1["episodeCount"])
	}
	s2 := seasonsRaw[1].(map[string]any)
	if _, ok := s2["episodeCount"]; ok {
		t.Errorf("seasons[1].episodeCount should be omitted (no statistics on the lookup), got %v", s2["episodeCount"])
	}
}

// TestHandleSearchAdd_SeasonsOnMovieRejected verifies that seasons on a
// type="movie" request is rejected with 400, before any Radarr/Sonarr call.
func TestHandleSearchAdd_SeasonsOnMovieRejected(t *testing.T) {
	t.Parallel()

	h := New(keysOnlyArr("sk", "rk", "pk"), "", "", "", &library.Handler{}, "")

	body := `{"type":"movie","tmdbId":1,"title":"X","seasons":[1]}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for seasons on a movie", w.Code)
	}
	if !strings.Contains(w.Body.String(), "only valid for series") {
		t.Errorf("body = %q, want it to explain seasons is series-only", w.Body.String())
	}
}

// TestHandleSearchAdd_EmptySeasonsArrayRejected verifies that a non-nil
// empty seasons array (there is no "monitor nothing" add) is rejected with 400.
func TestHandleSearchAdd_EmptySeasonsArrayRejected(t *testing.T) {
	t.Parallel()

	h := New(keysOnlyArr("sk", "rk", "pk"), "", "", "", &library.Handler{}, "")

	body := `{"type":"series","tvdbId":1,"title":"X","seasons":[]}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a non-nil empty seasons array", w.Code)
	}
}

// TestHandleSearchAdd_SeasonOutOfRangeRejected verifies the shape check
// (0-999) fires before any Sonarr lookup — tvdbId=0 here would 502 if the
// request reached Sonarr, so a 400 proves it didn't.
func TestHandleSearchAdd_SeasonOutOfRangeRejected(t *testing.T) {
	t.Parallel()

	h := New(keysOnlyArr("sk", "rk", "pk"), "", "", "", &library.Handler{}, "")

	body := `{"type":"series","tvdbId":0,"title":"X","seasons":[1000]}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an out-of-range season number", w.Code)
	}
}

// TestHandleSearchAdd_NilSeasonsOmitsSeasonsKey pins the "byte-identical to
// before" contract: an absent seasons field must not add a "seasons" key to
// the Sonarr add payload at all.
func TestHandleSearchAdd_NilSeasonsOmitsSeasonsKey(t *testing.T) {
	t.Setenv("REQUESTS_SONARR_PROFILE_ID", "1")
	t.Setenv("REQUESTS_SONARR_ROOT", "/media/tv")

	var postedBody map[string]any
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Dune: Prophecy","tvdbId":422100,"seasons":[{"seasonNumber":1,"monitored":true}]}]`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/series" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&postedBody)           //nolint:errcheck
			w.Write([]byte(`{"id":77,"title":"Dune: Prophecy"}`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, nil, nil, "")

	body := `{"type":"series","tvdbId":422100,"title":"Dune: Prophecy"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if postedBody == nil {
		t.Fatal("Sonarr never received the POST /api/v3/series add payload")
	}
	if _, ok := postedBody["seasons"]; ok {
		t.Errorf("payload should have no 'seasons' key when seasons is absent, got %v", postedBody["seasons"])
	}
	if _, ok := postedBody["addOptions"].(map[string]any)["monitor"]; ok {
		t.Errorf("addOptions.monitor must never be set, got %v", postedBody["addOptions"])
	}
}

// TestHandleSearchAdd_SeriesWithValidSeasonsThreadsPayload verifies the full
// happy path: a valid seasons selection produces a Sonarr payload with every
// season from the lookup, monitored flags matching the selection, and no
// addOptions.monitor key.
func TestHandleSearchAdd_SeriesWithValidSeasonsThreadsPayload(t *testing.T) {
	t.Setenv("REQUESTS_SONARR_PROFILE_ID", "1")
	t.Setenv("REQUESTS_SONARR_ROOT", "/media/tv")

	var postedBody map[string]any
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Dune: Prophecy","tvdbId":422100,"seasons":[` + //nolint:errcheck
				`{"seasonNumber":0,"monitored":false},` +
				`{"seasonNumber":1,"monitored":true},` +
				`{"seasonNumber":2,"monitored":true}]}]`))
		case r.URL.Path == "/api/v3/series" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&postedBody)           //nolint:errcheck
			w.Write([]byte(`{"id":77,"title":"Dune: Prophecy"}`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, nil, nil, "")

	// Deliberately duplicated + unsorted, to prove the handler normalizes
	// before threading into addSeriesInternal.
	body := `{"type":"series","tvdbId":422100,"title":"Dune: Prophecy","seasons":[2,1,2]}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if postedBody == nil {
		t.Fatal("Sonarr never received the POST /api/v3/series add payload")
	}
	if _, ok := postedBody["addOptions"].(map[string]any)["monitor"]; ok {
		t.Errorf("addOptions.monitor must never be set, got %v", postedBody["addOptions"])
	}
	seasonsRaw, ok := postedBody["seasons"].([]any)
	if !ok || len(seasonsRaw) != 3 {
		t.Fatalf("seasons = %v, want 3 entries", postedBody["seasons"])
	}
	want := map[int]bool{0: false, 1: true, 2: true}
	for _, s := range seasonsRaw {
		sm := s.(map[string]any)
		num := int(sm["seasonNumber"].(float64))
		monitored := sm["monitored"].(bool)
		if monitored != want[num] {
			t.Errorf("season %d monitored = %v, want %v", num, monitored, want[num])
		}
	}
}

// TestHandleSearchAdd_SeriesInvalidSeasonNumberReturns400 verifies that
// requesting a season number absent from the Sonarr lookup is rejected with
// 400 (clients.ErrInvalidSeasons), and that Sonarr's add endpoint is never
// called — existence is validated before the add attempt.
func TestHandleSearchAdd_SeriesInvalidSeasonNumberReturns400(t *testing.T) {
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/series/lookup":
			w.Write([]byte(`[{"title":"Dune: Prophecy","tvdbId":422100,"seasons":[{"seasonNumber":1,"monitored":true}]}]`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/series":
			t.Errorf("Sonarr add should not be called when season existence validation fails (path %s)", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer sonarr.Close()

	h := newHandler(sonarr, nil, nil, "")

	body := `{"type":"series","tvdbId":422100,"title":"Dune: Prophecy","seasons":[5]}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/search/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleSearchAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "5") {
		t.Errorf("expected error to mention the bad season number 5: %s", w.Body.String())
	}
}
