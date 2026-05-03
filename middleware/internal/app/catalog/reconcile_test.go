package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	arrclient "pelicula-api/internal/clients/arr"
)

// ── helpers ────────────────────────────────────────────────────────────────────

// newReconcileTestDB returns an in-memory catalog DB for reconcile tests.
func newReconcileTestDB(t *testing.T) *context.Context {
	t.Helper()
	return nil // just a marker; tests call testCatalogDB directly
}

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
	resp := map[string]any{
		"Items":            items,
		"TotalRecordCount": len(items),
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
