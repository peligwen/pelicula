package catalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	arrclient "pelicula-api/internal/clients/arr"
)

// ── helpers ────────────────────────────────────────────────────────────────────

// reconcileArrClient is a minimal ArrClient for reconcile tests backed by
// real arr.Client instances pointed at httptest servers.
type reconcileArrClient struct {
	radarr   *arrclient.Client
	prowlarr *arrclient.Client
}

func (s *reconcileArrClient) Keys() (sonarr, radarr, prowlarr string) {
	return "", "rk", ""
}
func (s *reconcileArrClient) SonarrClient() *arrclient.Client   { return arrclient.New("", "") }
func (s *reconcileArrClient) RadarrClient() *arrclient.Client   { return s.radarr }
func (s *reconcileArrClient) ProwlarrClient() *arrclient.Client { return s.prowlarr }

// buildJellyfinMovieResponse builds a Jellyfin /Users/{uid}/Items response
// containing the given movie items.
func buildJellyfinMovieResponse(items []map[string]any) []byte {
	return buildJellyfinMovieResponseWithTotal(items, len(items))
}

// buildJellyfinMovieResponseWithTotal builds a Jellyfin /Users/{uid}/Items
// response with an explicit TotalRecordCount, independent of len(items) —
// used to simulate a library response spanning multiple pages without having
// to fabricate hundreds of real items in a test.
func buildJellyfinMovieResponseWithTotal(items []map[string]any, total int) []byte {
	resp := map[string]any{
		"Items":            items,
		"TotalRecordCount": total,
	}
	b, _ := json.Marshal(resp)
	return b
}

// ── T1: orphan inserted on first run ──────────────────────────────────────────

func TestReconcileOrphans_OrphanInserted(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	const radarrRootPath = "/media/movies/"
	const orphanPath = "/media/movies/Orphan Test (2099)/Orphan Test (2099).mp4"

	// Radarr: one root folder, no movies with hasFile=true for this path.
	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{"id": 1, "path": radarrRootPath},
			})
		case "/api/v3/movie":
			// No movies known to Radarr.
			json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer radarrSrv.Close()

	// Jellyfin: one movie at the orphan path.
	jfMovies := buildJellyfinMovieResponse([]map[string]any{
		{
			"Id":             "jf-orphan-1",
			"Name":           "Orphan Test",
			"ProductionYear": 2099,
			"Path":           orphanPath,
			"ProviderIds":    map[string]any{},
		},
	})
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, path, _ string) ([]byte, error) {
			return jfMovies, nil
		},
	}

	svc := &reconcileArrClient{
		radarr:   arrclient.New(radarrSrv.URL, "rk"),
		prowlarr: arrclient.New("", ""),
	}

	result, err := ReconcileOrphans(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if result.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", result.Scanned)
	}
	if result.Added != 1 {
		t.Errorf("added = %d, want 1", result.Added)
	}

	// Verify the row was written with source='reconcile'.
	item, err := GetCatalogItemByFilePath(ctx, db, orphanPath)
	if err != nil {
		t.Fatalf("GetCatalogItemByFilePath: %v", err)
	}
	if item == nil {
		t.Fatal("expected catalog row, got nil")
	}
	if item.Source != "reconcile" {
		t.Errorf("source = %q, want 'reconcile'", item.Source)
	}
	if item.FilePath != orphanPath {
		t.Errorf("file_path = %q, want %q", item.FilePath, orphanPath)
	}
	if item.Tier != "library" {
		t.Errorf("tier = %q, want 'library'", item.Tier)
	}
}

// ── T2: idempotency — second run produces zero new rows ───────────────────────

func TestReconcileOrphans_Idempotent(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	const radarrRootPath = "/media/movies/"
	const orphanPath = "/media/movies/Idempotent Test (2000)/Idempotent Test (2000).mp4"

	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{"id": 1, "path": radarrRootPath},
			})
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer radarrSrv.Close()

	jfMovies := buildJellyfinMovieResponse([]map[string]any{
		{
			"Id":             "jf-idempotent-1",
			"Name":           "Idempotent Test",
			"ProductionYear": 2000,
			"Path":           orphanPath,
			"ProviderIds":    map[string]any{},
		},
	})
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, path, _ string) ([]byte, error) {
			return jfMovies, nil
		},
	}

	svc := &reconcileArrClient{
		radarr:   arrclient.New(radarrSrv.URL, "rk"),
		prowlarr: arrclient.New("", ""),
	}

	// First run.
	r1, err := ReconcileOrphans(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if r1.Added != 1 {
		t.Fatalf("first run: added = %d, want 1", r1.Added)
	}

	// Second run — must be a no-op.
	r2, err := ReconcileOrphans(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if r2.Added != 0 {
		t.Errorf("second run: added = %d, want 0 (idempotent)", r2.Added)
	}
	if r2.Scanned != 1 {
		t.Errorf("second run: scanned = %d, want 1", r2.Scanned)
	}
}

// ── T3: arr-has-file skip — item traceable via Radarr is NOT inserted ─────────

func TestReconcileOrphans_ArrHasFileSkipped(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	const radarrRootPath = "/media/movies/"
	const moviePath = "/media/movies/Radarr Known (2020)/Radarr Known (2020).mp4"

	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{"id": 1, "path": radarrRootPath},
			})
		case "/api/v3/movie":
			// Radarr knows about this movie and hasFile=true.
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{
					"id":      float64(101),
					"tmdbId":  float64(5050),
					"title":   "Radarr Known",
					"year":    float64(2020),
					"hasFile": true,
					"movieFile": map[string]any{
						"path": moviePath,
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer radarrSrv.Close()

	jfMovies := buildJellyfinMovieResponse([]map[string]any{
		{
			"Id":             "jf-radarr-known-1",
			"Name":           "Radarr Known",
			"ProductionYear": 2020,
			"Path":           moviePath,
			"ProviderIds":    map[string]any{"Tmdb": "5050"},
		},
	})
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, path, _ string) ([]byte, error) {
			return jfMovies, nil
		},
	}

	svc := &reconcileArrClient{
		radarr:   arrclient.New(radarrSrv.URL, "rk"),
		prowlarr: arrclient.New("", ""),
	}

	result, err := ReconcileOrphans(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("added = %d, want 0 (radarr has the file)", result.Added)
	}
	if result.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", result.Scanned)
	}
}

// ── T4: already-cataloged skip — existing catalog row is NOT re-inserted ──────

func TestReconcileOrphans_AlreadyCatalogedSkipped(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	const radarrRootPath = "/media/movies/"
	const moviePath = "/media/movies/Already Cataloged (2015)/Already Cataloged (2015).mp4"

	// Pre-seed catalog row (simulating a prior BackfillFromArr run).
	_, err := UpsertCatalogItem(ctx, db, CatalogItem{
		Type:     "movie",
		TmdbID:   7777,
		ArrType:  "radarr",
		ArrID:    200,
		Title:    "Already Cataloged",
		Year:     2015,
		Tier:     "library",
		FilePath: moviePath,
	})
	if err != nil {
		t.Fatalf("seed catalog item: %v", err)
	}

	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{"id": 1, "path": radarrRootPath},
			})
		case "/api/v3/movie":
			// Radarr does NOT have this movie (it was added directly to catalog).
			json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer radarrSrv.Close()

	jfMovies := buildJellyfinMovieResponse([]map[string]any{
		{
			"Id":             "jf-already-1",
			"Name":           "Already Cataloged",
			"ProductionYear": 2015,
			"Path":           moviePath,
			"ProviderIds":    map[string]any{"Tmdb": "7777"},
		},
	})
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, path, _ string) ([]byte, error) {
			return jfMovies, nil
		},
	}

	svc := &reconcileArrClient{
		radarr:   arrclient.New(radarrSrv.URL, "rk"),
		prowlarr: arrclient.New("", ""),
	}

	result, err := ReconcileOrphans(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("added = %d, want 0 (already in catalog)", result.Added)
	}

	// Ensure the original row was NOT replaced (source should still be default 'arr').
	item, err := GetCatalogItemByFilePath(ctx, db, moviePath)
	if err != nil {
		t.Fatalf("GetCatalogItemByFilePath: %v", err)
	}
	if item == nil {
		t.Fatal("expected catalog row, got nil")
	}
	if item.Source != "arr" {
		t.Errorf("source = %q, want 'arr' (original row must not be touched)", item.Source)
	}
}

// ── T5: items outside radarr root folders are not touched ────────────────────

func TestReconcileOrphans_OutsideRootFolderSkipped(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	const radarrRootPath = "/media/movies/"
	const outsidePath = "/mnt/nas/personal/home-video.mp4"

	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{"id": 1, "path": radarrRootPath},
			})
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer radarrSrv.Close()

	// Jellyfin item is outside the Radarr root folder.
	jfMovies := buildJellyfinMovieResponse([]map[string]any{
		{
			"Id":             "jf-outside-1",
			"Name":           "Home Video",
			"ProductionYear": 2010,
			"Path":           outsidePath,
			"ProviderIds":    map[string]any{},
		},
	})
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, path, _ string) ([]byte, error) {
			return jfMovies, nil
		},
	}

	svc := &reconcileArrClient{
		radarr:   arrclient.New(radarrSrv.URL, "rk"),
		prowlarr: arrclient.New("", ""),
	}

	result, err := ReconcileOrphans(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if result.Added != 0 {
		t.Errorf("added = %d, want 0 (outside radarr root folder)", result.Added)
	}
	if result.Scanned != 0 {
		t.Errorf("scanned = %d, want 0 (item should be filtered out)", result.Scanned)
	}
}

// ── T6: MWA-3 — pagination across StartIndex ──────────────────────────────────

// TestReconcileOrphans_PaginatesAcrossStartIndex verifies that ReconcileOrphans
// pages through the full Jellyfin library via StartIndex instead of stopping
// after the first page. The library reports 5 total movies split across two
// pages (2 then 3) — before the MWA-3 fix, only the first page was ever
// fetched (no StartIndex was sent at all), so item 3-5 would never be scanned.
func TestReconcileOrphans_PaginatesAcrossStartIndex(t *testing.T) {
	t.Parallel()
	db := testCatalogDB(t)
	ctx := context.Background()

	const radarrRootPath = "/media/movies/"

	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{"id": 1, "path": radarrRootPath},
			})
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer radarrSrv.Close()

	movieItem := func(n int) map[string]any {
		name := "Movie " + strconv.Itoa(n)
		return map[string]any{
			"Id":             "jf-" + strconv.Itoa(n),
			"Name":           name,
			"ProductionYear": 2000 + n,
			"Path":           radarrRootPath + name + "/" + name + ".mp4",
			"ProviderIds":    map[string]any{},
		}
	}
	// Total is 5 regardless of how many items each page actually carries —
	// the mock doesn't need 500+ real items to force a second page request.
	page1 := buildJellyfinMovieResponseWithTotal([]map[string]any{movieItem(1), movieItem(2)}, 5)
	page2 := buildJellyfinMovieResponseWithTotal([]map[string]any{movieItem(3), movieItem(4), movieItem(5)}, 5)

	var startIndexesSeen []string
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, path, _ string) ([]byte, error) {
			u, err := url.Parse(path)
			if err != nil {
				t.Fatalf("parse jellyfin path %q: %v", path, err)
			}
			si := u.Query().Get("StartIndex")
			startIndexesSeen = append(startIndexesSeen, si)
			if si == "0" {
				return page1, nil
			}
			return page2, nil
		},
	}

	svc := &reconcileArrClient{
		radarr:   arrclient.New(radarrSrv.URL, "rk"),
		prowlarr: arrclient.New("", ""),
	}

	result, err := ReconcileOrphans(ctx, db, jf, svc)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if result.Scanned != 5 {
		t.Errorf("scanned = %d, want 5 (across both pages)", result.Scanned)
	}
	if result.Added != 5 {
		t.Errorf("added = %d, want 5", result.Added)
	}

	if len(startIndexesSeen) != 2 {
		t.Fatalf("expected 2 JellyfinGet calls (one per page), got %d: %v", len(startIndexesSeen), startIndexesSeen)
	}
	if startIndexesSeen[0] != "0" {
		t.Errorf("first page StartIndex = %q, want \"0\"", startIndexesSeen[0])
	}
	wantSecond := strconv.Itoa(reconcilePageSize)
	if startIndexesSeen[1] != wantSecond {
		t.Errorf("second page StartIndex = %q, want %q", startIndexesSeen[1], wantSecond)
	}

	for n := 1; n <= 5; n++ {
		path := radarrRootPath + "Movie " + strconv.Itoa(n) + "/Movie " + strconv.Itoa(n) + ".mp4"
		item, err := GetCatalogItemByFilePath(ctx, db, path)
		if err != nil {
			t.Fatalf("GetCatalogItemByFilePath(%q): %v", path, err)
		}
		if item == nil {
			t.Errorf("expected a catalog row for %q (from the second page), got none", path)
		}
	}
}

// ── T7: MWA-3 — near-cap warning ──────────────────────────────────────────────

// TestReconcileOrphans_WarnsNearLibraryCap verifies that ReconcileOrphans
// emits the same near-cap slog.Warn pattern fetchJellyfinLibrary (sync.go)
// already uses, when the scanned item count reaches the configured safety cap.
func TestReconcileOrphans_WarnsNearLibraryCap(t *testing.T) {
	// Not parallel: mutates the process-wide slog default.
	db := testCatalogDB(t)
	ctx := context.Background()

	t.Setenv("RECONCILE_LIBRARY_LIMIT", "2")

	const radarrRootPath = "/media/movies/"

	radarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{"id": 1, "path": radarrRootPath},
			})
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer radarrSrv.Close()

	jfMovies := buildJellyfinMovieResponse([]map[string]any{
		{"Id": "jf-1", "Name": "Movie 1", "ProductionYear": 2001, "Path": radarrRootPath + "Movie 1/Movie 1.mp4", "ProviderIds": map[string]any{}},
		{"Id": "jf-2", "Name": "Movie 2", "ProductionYear": 2002, "Path": radarrRootPath + "Movie 2/Movie 2.mp4", "ProviderIds": map[string]any{}},
	})
	jf := &stubJfClient{
		apiKey: "jf-key",
		userID: "user-1",
		doGet: func(_ context.Context, path, _ string) ([]byte, error) {
			return jfMovies, nil
		},
	}

	svc := &reconcileArrClient{
		radarr:   arrclient.New(radarrSrv.URL, "rk"),
		prowlarr: arrclient.New("", ""),
	}

	var capture warnCapture
	orig := slog.Default()
	slog.SetDefault(slog.New(&capture))
	t.Cleanup(func() { slog.SetDefault(orig) })

	if _, err := ReconcileOrphans(ctx, db, jf, svc); err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}

	if !capture.hasWarnContaining("jellyfin library scan near cap") {
		t.Error("expected a near-cap slog.Warn when the scanned count reaches RECONCILE_LIBRARY_LIMIT")
	}
}
