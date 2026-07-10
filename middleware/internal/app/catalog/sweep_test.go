package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	arrclient "pelicula-api/internal/clients/arr"
)

// ── helpers ────────────────────────────────────────────────────────────────────

// sweepArrClient is an ArrClient fake with configurable keys, backed by real
// arr.Client instances pointed at httptest servers.
type sweepArrClient struct {
	sonarrKey, radarrKey string
	sonarr, radarr       *arrclient.Client
}

func (s *sweepArrClient) Keys() (sonarr, radarr, prowlarr string) {
	return s.sonarrKey, s.radarrKey, ""
}
func (s *sweepArrClient) SonarrClient() *arrclient.Client   { return s.sonarr }
func (s *sweepArrClient) RadarrClient() *arrclient.Client   { return s.radarr }
func (s *sweepArrClient) ProwlarrClient() *arrclient.Client { return arrclient.New("", "") }

// arrJSONServer serves fixed JSON bodies per path; unknown paths 404.
func arrJSONServer(t *testing.T, routes map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// jfMovieStub returns a stubJfClient whose movie scan yields the given paths.
func jfMovieStub(paths ...string) *stubJfClient {
	items := make([]map[string]any, 0, len(paths))
	for i, p := range paths {
		items = append(items, map[string]any{
			"Id": "jf-" + p, "Name": "Movie", "ProductionYear": 2000 + i,
			"Path": p, "ProviderIds": map[string]any{},
		})
	}
	body := buildJellyfinMovieResponse(items)
	return &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, _, _ string) ([]byte, error) {
			return body, nil
		},
	}
}

const staleTime = "2026-01-01T00:00:00Z"

// seedSweepRow inserts a catalog row with explicit identity, source and
// updated_at, bypassing Upsert so tests control staleness.
func seedSweepRow(t *testing.T, db *sql.DB, id, typ, parentID string, tmdbID, tvdbID, arrID int, arrType, source, filePath, updatedAt string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO catalog_items
			(id, type, parent_id, tmdb_id, tvdb_id, arr_id, arr_type,
			 title, tier, file_path, source, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,'library',?,?,?,?)
	`, id, typ, parentID, tmdbID, tvdbID, arrID, arrType,
		"row "+id, filePath, source, updatedAt, updatedAt); err != nil {
		t.Fatalf("seed row %s: %v", id, err)
	}
}

func rowExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM catalog_items WHERE id=?`, id).Scan(&n); err != nil {
		t.Fatalf("row lookup %s: %v", id, err)
	}
	return n > 0
}

// ── movie sweep ────────────────────────────────────────────────────────────────

func TestSweepStale_RemovesMovieGoneFromRadarr(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	// Radarr tracks tmdb 100 and (by arr_id only) 42; tmdb 999 is gone.
	radarrSrv := arrJSONServer(t, map[string]any{
		"/api/v3/movie": []map[string]any{
			{"id": 1, "tmdbId": 100, "title": "Kept", "year": 2001},
			{"id": 42, "tmdbId": 0, "title": "Kept by arr id", "year": 2002},
		},
	})
	svc := &sweepArrClient{
		radarrKey: "rk",
		radarr:    arrclient.New(radarrSrv.URL, "rk"),
		sonarr:    arrclient.New("", ""),
	}

	seedSweepRow(t, db, "kept_tmdb", "movie", "", 100, 0, 7, "radarr", "arr", "", staleTime)
	seedSweepRow(t, db, "kept_arrid", "movie", "", 0, 0, 42, "radarr", "arr", "", staleTime)
	seedSweepRow(t, db, "gone", "movie", "", 999, 0, 9, "radarr", "arr", "", staleTime)
	seedSweepRow(t, db, "untestable", "movie", "", 0, 0, 0, "radarr", "arr", "", staleTime)

	result, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.MoviesDeleted != 1 {
		t.Errorf("expected 1 movie deleted, got %d", result.MoviesDeleted)
	}
	if rowExists(t, db, "gone") {
		t.Error("movie gone from Radarr survived the sweep")
	}
	for _, id := range []string{"kept_tmdb", "kept_arrid", "untestable"} {
		if !rowExists(t, db, id) {
			t.Errorf("row %s was deleted but should have been kept", id)
		}
	}
}

func TestSweepStale_GraceWindowSparesFreshRows(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	radarrSrv := arrJSONServer(t, map[string]any{
		"/api/v3/movie": []map[string]any{
			{"id": 1, "tmdbId": 100, "title": "Alive", "year": 2001},
		},
	})
	svc := &sweepArrClient{
		radarrKey: "rk",
		radarr:    arrclient.New(radarrSrv.URL, "rk"),
		sonarr:    arrclient.New("", ""),
	}

	// Doomed by the liveness test but written just now — the grace window
	// must spare it.
	fresh := time.Now().UTC().Format(time.RFC3339)
	seedSweepRow(t, db, "fresh_gone", "movie", "", 999, 0, 9, "radarr", "arr", "", fresh)

	result, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.MoviesDeleted != 0 {
		t.Errorf("expected 0 deletions, got %d", result.MoviesDeleted)
	}
	if !rowExists(t, db, "fresh_gone") {
		t.Error("grace window failed: freshly written row was swept")
	}
}

func TestSweepStale_RadarrFetchErrorSkipsMovieSweep(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	// Radarr server that always 500s.
	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(radarrSrv.Close)
	svc := &sweepArrClient{
		radarrKey: "rk",
		radarr:    arrclient.New(radarrSrv.URL, "rk"),
		sonarr:    arrclient.New("", ""),
	}

	seedSweepRow(t, db, "kept_on_error", "movie", "", 999, 0, 9, "radarr", "arr", "", staleTime)

	result, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.MoviesDeleted != 0 || !rowExists(t, db, "kept_on_error") {
		t.Error("movie sweep ran despite Radarr fetch failure")
	}
}

func TestSweepStale_EmptyRadarrSkipsMovieSweep(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	radarrSrv := arrJSONServer(t, map[string]any{
		"/api/v3/movie": []map[string]any{},
	})
	svc := &sweepArrClient{
		radarrKey: "rk",
		radarr:    arrclient.New(radarrSrv.URL, "rk"),
		sonarr:    arrclient.New("", ""),
	}

	seedSweepRow(t, db, "kept_on_empty", "movie", "", 999, 0, 9, "radarr", "arr", "", staleTime)

	result, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.MoviesDeleted != 0 || !rowExists(t, db, "kept_on_empty") {
		t.Error("empty Radarr response was treated as an emptied library")
	}
}

// ── series sweep + cascade ─────────────────────────────────────────────────────

func TestSweepStale_RemovesSeriesAndCascadesChildren(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	sonarrSrv := arrJSONServer(t, map[string]any{
		"/api/v3/series": []map[string]any{
			{"id": 5, "tvdbId": 500, "tmdbId": 0, "title": "Kept Show", "year": 2010},
		},
	})
	svc := &sweepArrClient{
		sonarrKey: "sk",
		sonarr:    arrclient.New(sonarrSrv.URL, "sk"),
		radarr:    arrclient.New("", ""),
	}

	seedSweepRow(t, db, "kept_series", "series", "", 0, 500, 5, "sonarr", "arr", "", staleTime)
	seedSweepRow(t, db, "kept_season", "season", "kept_series", 0, 0, 0, "", "arr", "", staleTime)
	seedSweepRow(t, db, "gone_series", "series", "", 0, 600, 6, "sonarr", "arr", "", staleTime)
	seedSweepRow(t, db, "gone_season", "season", "gone_series", 0, 0, 0, "", "arr", "", staleTime)
	seedSweepRow(t, db, "gone_ep", "episode", "gone_season", 0, 0, 0, "sonarr", "arr", "/tv/x.mkv", staleTime)

	result, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.SeriesDeleted != 1 {
		t.Errorf("expected 1 series deleted, got %d", result.SeriesDeleted)
	}
	if result.ChildrenDeleted != 2 {
		t.Errorf("expected 2 children deleted, got %d", result.ChildrenDeleted)
	}
	for _, id := range []string{"gone_series", "gone_season", "gone_ep"} {
		if rowExists(t, db, id) {
			t.Errorf("row %s survived the sweep", id)
		}
	}
	for _, id := range []string{"kept_series", "kept_season"} {
		if !rowExists(t, db, id) {
			t.Errorf("row %s was deleted but should have been kept", id)
		}
	}
}

func TestSweepStale_UnconfiguredSonarrSkipsSeriesSweep(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	svc := &sweepArrClient{
		// No keys at all: both *arr sweeps must skip.
		sonarr: arrclient.New("", ""),
		radarr: arrclient.New("", ""),
	}

	seedSweepRow(t, db, "kept_series", "series", "", 0, 600, 6, "sonarr", "arr", "", staleTime)
	seedSweepRow(t, db, "kept_movie", "movie", "", 999, 0, 9, "radarr", "arr", "", staleTime)

	result, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.total() != 0 {
		t.Errorf("expected no deletions, got %+v", result)
	}
}

// ── reconcile-row sweep ────────────────────────────────────────────────────────

func TestSweepStale_ReconcileRowsFollowJellyfinPresence(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	svc := &sweepArrClient{
		sonarr: arrclient.New("", ""),
		radarr: arrclient.New("", ""),
	}
	jf := jfMovieStub("/media/movies/Kept (2001)/Kept.mp4")

	seedSweepRow(t, db, "rec_kept", "movie", "", 0, 0, 0, "radarr", "reconcile",
		"/media/movies/Kept (2001)/Kept.mp4", staleTime)
	seedSweepRow(t, db, "rec_gone", "movie", "", 0, 0, 0, "radarr", "reconcile",
		"/media/movies/Gone (2002)/Gone.mp4", staleTime)

	result, err := SweepStale(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.ReconcileDeleted != 1 {
		t.Errorf("expected 1 reconcile row deleted, got %d", result.ReconcileDeleted)
	}
	if rowExists(t, db, "rec_gone") {
		t.Error("reconcile row gone from Jellyfin survived the sweep")
	}
	if !rowExists(t, db, "rec_kept") {
		t.Error("reconcile row still in Jellyfin was deleted")
	}
}

func TestSweepStale_IncompleteJellyfinScanSkipsReconcileSweep(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	t.Setenv("RECONCILE_LIBRARY_LIMIT", "1")
	db := testCatalogDB(t)
	ctx := context.Background()

	svc := &sweepArrClient{
		sonarr: arrclient.New("", ""),
		radarr: arrclient.New("", ""),
	}

	// Two-movie library but a cap of 1: the scan is incomplete, so absence
	// proves nothing and no reconcile row may be swept.
	items := []map[string]any{
		{"Id": "jf-1", "Name": "One", "Path": "/media/movies/One/One.mp4", "ProviderIds": map[string]any{}},
	}
	body := buildJellyfinMovieResponseWithTotal(items, 2)
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, _, _ string) ([]byte, error) {
			return body, nil
		},
	}

	seedSweepRow(t, db, "rec_unproven", "movie", "", 0, 0, 0, "radarr", "reconcile",
		"/media/movies/Two (2002)/Two.mp4", staleTime)

	result, err := SweepStale(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if result.ReconcileDeleted != 0 || !rowExists(t, db, "rec_unproven") {
		t.Error("reconcile row swept despite incomplete Jellyfin scan")
	}
}

// ── idempotency ────────────────────────────────────────────────────────────────

func TestSweepStale_SecondPassDeletesNothing(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	radarrSrv := arrJSONServer(t, map[string]any{
		"/api/v3/movie": []map[string]any{
			{"id": 1, "tmdbId": 100, "title": "Kept", "year": 2001},
		},
	})
	svc := &sweepArrClient{
		radarrKey: "rk",
		radarr:    arrclient.New(radarrSrv.URL, "rk"),
		sonarr:    arrclient.New("", ""),
	}

	seedSweepRow(t, db, "kept", "movie", "", 100, 0, 1, "radarr", "arr", "", staleTime)
	seedSweepRow(t, db, "gone", "movie", "", 999, 0, 9, "radarr", "arr", "", staleTime)

	first, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("first SweepStale: %v", err)
	}
	if first.MoviesDeleted != 1 {
		t.Fatalf("expected 1 deletion on first pass, got %d", first.MoviesDeleted)
	}

	second, err := SweepStale(ctx, db, jfMovieStub(), svc)
	if err != nil {
		t.Fatalf("second SweepStale: %v", err)
	}
	if second.total() != 0 {
		t.Errorf("expected idempotent second pass, got %+v", second)
	}
}
