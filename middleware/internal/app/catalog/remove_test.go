package catalog_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pelicula-api/internal/app/catalog"
	repocat "pelicula-api/internal/repo/catalog"
)

// newRemoveTestHandler builds a catalog.Handler backed by real httptest
// servers plus a real in-memory catalog DB, for HandleCatalogRemove tests.
func newRemoveTestHandler(t *testing.T, radarrSrv, sonarrSrv *httptest.Server, proculaAPIKey string) *catalog.Handler {
	t.Helper()
	h := newTestHandler(radarrSrv, sonarrSrv, nil, "rk", "sk")
	db, err := catalog.OpenCatalogDB(":memory:")
	if err != nil {
		t.Fatalf("OpenCatalogDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h.DB = db
	h.ProculaAPIKey = proculaAPIKey
	return h
}

func postRemove(h *catalog.Handler, body, apiKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/remove", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	w := httptest.NewRecorder()
	h.HandleCatalogRemove(w, req)
	return w
}

func TestHandleCatalogRemove_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := newRemoveTestHandler(t, nil, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/remove", nil)
	w := httptest.NewRecorder()
	h.HandleCatalogRemove(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleCatalogRemove_RejectsMissingOrWrongAPIKey(t *testing.T) {
	t.Parallel()
	h := newRemoveTestHandler(t, nil, nil, "secret-key")

	body := `{"arr_type":"radarr","arr_id":1}`

	// No header at all.
	w := postRemove(h, body, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing key: status = %d, want 401", w.Code)
	}

	// Wrong key.
	w = postRemove(h, body, "wrong-key")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", w.Code)
	}
}

func TestHandleCatalogRemove_ValidatesBody(t *testing.T) {
	t.Parallel()
	h := newRemoveTestHandler(t, nil, nil, "")

	// Invalid arr_type.
	w := postRemove(h, `{"arr_type":"plex","arr_id":1}`, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid arr_type: status = %d, want 400", w.Code)
	}

	// Missing/zero arr_id.
	w = postRemove(h, `{"arr_type":"radarr","arr_id":0}`, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing arr_id: status = %d, want 400", w.Code)
	}

	// Malformed JSON.
	w = postRemove(h, `not-json`, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: status = %d, want 400", w.Code)
	}
}

// TestHandleCatalogRemove_HappyPath_Radarr verifies the full success path for
// a movie: the handler fetches the movie (for its file path + title) BEFORE
// deleting, issues the DELETE with deleteFiles=true&addImportExclusion=false,
// purges the matching catalog_items row, and returns the documented shape.
func TestHandleCatalogRemove_HappyPath_Radarr(t *testing.T) {
	t.Parallel()

	var deleteQuery string
	var deleteHit bool
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/movie/42":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":42,"title":"The Matrix","movieFile":{"path":"/media/movies/The Matrix/movie.mkv"}}`)) //nolint:errcheck
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/movie/42":
			deleteHit = true
			deleteQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer radarr.Close()

	h := newRemoveTestHandler(t, radarr, nil, "")

	// Seed a catalog_items row that should be purged by the remove.
	store := repocat.New(h.DB)
	id, err := store.Upsert(t.Context(), repocat.Item{
		Type: "movie", ArrID: 42, ArrType: "radarr",
		Title: "The Matrix", Year: 1999, Tier: "library",
		FilePath: "/media/movies/The Matrix/movie.mkv",
	})
	if err != nil {
		t.Fatalf("seed catalog item: %v", err)
	}

	w := postRemove(h, `{"arr_type":"radarr","arr_id":42}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if !deleteHit {
		t.Fatal("radarr DELETE was never called")
	}
	if !strings.Contains(deleteQuery, "deleteFiles=true") || !strings.Contains(deleteQuery, "addImportExclusion=false") {
		t.Errorf("delete query = %q, want deleteFiles=true&addImportExclusion=false", deleteQuery)
	}

	var resp struct {
		Removed   bool     `json:"removed"`
		ArrType   string   `json:"arr_type"`
		ArrID     int      `json:"arr_id"`
		Title     string   `json:"title"`
		FilePaths []string `json:"file_paths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if !resp.Removed {
		t.Error("removed = false, want true")
	}
	if resp.ArrType != "radarr" || resp.ArrID != 42 {
		t.Errorf("arr_type/arr_id = %s/%d, want radarr/42", resp.ArrType, resp.ArrID)
	}
	if resp.Title != "The Matrix" {
		t.Errorf("title = %q, want %q", resp.Title, "The Matrix")
	}
	if len(resp.FilePaths) != 1 || resp.FilePaths[0] != "/media/movies/The Matrix/movie.mkv" {
		t.Errorf("file_paths = %v", resp.FilePaths)
	}

	// catalog_items row must be gone.
	if got, _ := store.Get(t.Context(), id); got != nil {
		t.Error("catalog_items row survived HandleCatalogRemove")
	}
}

// TestHandleCatalogRemove_HappyPath_Sonarr verifies the series path: episode
// files are gathered from GetEpisodeFiles, the series title from
// GetSeriesByID, and the DELETE targets /api/v3/series/{id}. Also verifies
// the series→season→episode cascade fires (DeleteByArr + DeleteOrphanedChildren).
func TestHandleCatalogRemove_HappyPath_Sonarr(t *testing.T) {
	t.Parallel()

	var deleteHit bool
	var deleteQuery string
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/series/7":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":7,"title":"Breaking Bad"}`)) //nolint:errcheck
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/episodefile":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":1,"path":"/media/bb/s01e01.mkv"},{"id":2,"path":"/media/bb/s01e02.mkv"}]`)) //nolint:errcheck
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/series/7":
			deleteHit = true
			deleteQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer sonarr.Close()

	h := newRemoveTestHandler(t, nil, sonarr, "")

	store := repocat.New(h.DB)
	seriesID, err := store.Upsert(t.Context(), repocat.Item{
		Type: "series", ArrID: 7, ArrType: "sonarr",
		Title: "Breaking Bad", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("seed series: %v", err)
	}
	seasonID, err := store.Upsert(t.Context(), repocat.Item{
		Type: "season", ParentID: seriesID, SeasonNumber: 1,
		Title: "Breaking Bad Season 1", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("seed season: %v", err)
	}
	epID, err := store.Upsert(t.Context(), repocat.Item{
		Type: "episode", ParentID: seasonID, EpisodeID: 55,
		SeasonNumber: 1, EpisodeNumber: 1, ArrType: "sonarr",
		FilePath: "/media/bb/s01e01.mkv", Title: "Pilot", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("seed episode: %v", err)
	}

	w := postRemove(h, `{"arr_type":"sonarr","arr_id":7}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if !deleteHit {
		t.Fatal("sonarr DELETE was never called")
	}
	if !strings.Contains(deleteQuery, "deleteFiles=true") || !strings.Contains(deleteQuery, "addImportExclusion=false") {
		t.Errorf("delete query = %q", deleteQuery)
	}

	var resp struct {
		Removed   bool     `json:"removed"`
		Title     string   `json:"title"`
		FilePaths []string `json:"file_paths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Removed {
		t.Error("removed = false, want true")
	}
	if resp.Title != "Breaking Bad" {
		t.Errorf("title = %q, want %q", resp.Title, "Breaking Bad")
	}
	if len(resp.FilePaths) != 2 {
		t.Errorf("file_paths = %v, want 2 entries", resp.FilePaths)
	}

	// Whole chain (series, season, episode) must be gone via
	// DeleteByArr + DeleteOrphanedChildren.
	if got, _ := store.Get(t.Context(), seriesID); got != nil {
		t.Error("series row survived")
	}
	if got, _ := store.Get(t.Context(), seasonID); got != nil {
		t.Error("season row survived cascade")
	}
	if got, _ := store.Get(t.Context(), epID); got != nil {
		t.Error("episode row survived cascade")
	}
}

// TestHandleCatalogRemove_ToleratesArrNotFound verifies a 404 from the *arr
// DELETE (title already gone) is treated as success, not an error —
// idempotency for retries.
func TestHandleCatalogRemove_ToleratesArrNotFound(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/movie/99":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/movie/99":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer radarr.Close()

	h := newRemoveTestHandler(t, radarr, nil, "")

	w := postRemove(h, `{"arr_type":"radarr","arr_id":99}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s, want 200 (404 from *arr is tolerated)", w.Code, w.Body.String())
	}
	var resp struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Removed {
		t.Error("removed = false, want true even when *arr already had no such title")
	}
}

// TestHandleCatalogRemove_ArrDeleteFailurePropagates verifies a genuine (non-404)
// *arr failure surfaces as an error response rather than a false "removed:true".
func TestHandleCatalogRemove_ArrDeleteFailurePropagates(t *testing.T) {
	t.Parallel()

	// 400 is a permanent, non-retried failure — keeps the test fast (the arr
	// client's default RetryPolicy retries 5xx three times with backoff).
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/movie/5":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":5,"title":"Foo"}`)) //nolint:errcheck
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/movie/5":
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer radarr.Close()

	h := newRemoveTestHandler(t, radarr, nil, "")

	w := postRemove(h, `{"arr_type":"radarr","arr_id":5}`, "")
	if w.Code == http.StatusOK {
		t.Fatalf("status = 200, want an error status; body=%s", w.Body.String())
	}
}

// TestHandleCatalogRemove_EmptyKeySkipsAuth verifies back-compat: when no
// PROCULA_API_KEY is configured, the endpoint accepts requests with no
// X-API-Key header at all (same behavior as HandleJellyfinRefresh).
func TestHandleCatalogRemove_EmptyKeySkipsAuth(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer radarr.Close()

	h := newRemoveTestHandler(t, radarr, nil, "") // no key configured

	w := postRemove(h, `{"arr_type":"radarr","arr_id":1}`, "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when no key is configured", w.Code)
	}
}
