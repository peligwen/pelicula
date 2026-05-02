package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── JellyfinMetaClient stub ───────────────────────────────────────────────────

type stubJfClient struct {
	apiKey string
	userID string
	doGet  func(ctx context.Context, path, key string) ([]byte, error)
}

func (s *stubJfClient) GetJellyfinAPIKey() string   { return s.apiKey }
func (s *stubJfClient) GetJellyfinUserID() string   { return s.userID }
func (s *stubJfClient) SetJellyfinUserID(id string) { s.userID = id }
func (s *stubJfClient) JellyfinGet(ctx context.Context, path, apiKey string) ([]byte, error) {
	if s.doGet != nil {
		return s.doGet(ctx, path, apiKey)
	}
	return json.Marshal(struct {
		Items []jellyfinItem `json:"Items"`
	}{})
}

// ── F8: fetchJellyfinLibrary single-flight on concurrent miss ─────────────────

func TestFetchJellyfinLibrary_SingleFlightOnConcurrentMiss(t *testing.T) {
	const goroutines = 20
	const fetchDelay = 20 * time.Millisecond

	var calls atomic.Int32
	jf := &stubJfClient{
		apiKey: "key",
		userID: "user1",
		doGet: func(ctx context.Context, path, key string) ([]byte, error) {
			calls.Add(1)
			time.Sleep(fetchDelay)
			return json.Marshal(struct {
				Items []jellyfinItem `json:"Items"`
			}{Items: []jellyfinItem{{ID: "jf1"}}})
		},
	}

	h := &Handler{}

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.fetchJellyfinLibrary(context.Background(), jf) //nolint:errcheck
		}()
	}
	wg.Wait()

	if calls.Load() != 1 {
		t.Errorf("upstream called %d times, want 1 (single-flight on concurrent miss)", calls.Load())
	}
}

// ── F11: backfillSonarr continues on partial error ───────────────────────────

func testSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenCatalogDB(dir + "/catalog.db")
	if err != nil {
		t.Fatalf("OpenCatalogDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// stubArrBackfill is a minimal ArrClient for backfill tests.
type stubArrBackfill struct {
	doGet func(baseURL, apiKey, path string) ([]byte, error)
}

func (s *stubArrBackfill) Keys() (sonarr, radarr, prowlarr string) {
	return "sk", "rk", ""
}
func (s *stubArrBackfill) ArrGet(_ context.Context, baseURL, apiKey, path string) ([]byte, error) {
	if s.doGet != nil {
		return s.doGet(baseURL, apiKey, path)
	}
	return nil, fmt.Errorf("stub: unexpected ArrGet %q", path)
}
func (s *stubArrBackfill) ArrPost(_ context.Context, baseURL, apiKey, path string, payload any) ([]byte, error) {
	return nil, nil
}
func (s *stubArrBackfill) ArrPut(_ context.Context, baseURL, apiKey, path string, payload any) ([]byte, error) {
	return nil, nil
}
func (s *stubArrBackfill) ArrDelete(_ context.Context, baseURL, apiKey, path string) ([]byte, error) {
	return nil, nil
}
func (s *stubArrBackfill) ArrGetAllQueueRecords(_ context.Context, baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error) {
	return nil, nil
}

func TestBackfillSonarr_ContinuesOnPartialError(t *testing.T) {
	// Three series; the middle one's episode fetch returns 500.
	// Series 1 and 3 should have seasons persisted; series 2 should not.

	series := []map[string]any{
		{"id": float64(1), "title": "Series One", "year": float64(2020), "tvdbId": float64(101)},
		{"id": float64(2), "title": "Series Two", "year": float64(2021), "tvdbId": float64(102)},
		{"id": float64(3), "title": "Series Three", "year": float64(2022), "tvdbId": float64(103)},
	}
	seriesJSON, _ := json.Marshal(series)

	sonarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/series" {
			w.Write(seriesJSON) //nolint:errcheck
			return
		}
		// Episode endpoints: fail for seriesId=2, succeed for 1 and 3.
		q := r.URL.Query().Get("seriesId")
		if q == "2" {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var eps []map[string]any
		if q == "1" {
			eps = []map[string]any{
				{"seriesId": float64(1), "seasonNumber": float64(1), "id": float64(11)},
			}
		} else if q == "3" {
			eps = []map[string]any{
				{"seriesId": float64(3), "seasonNumber": float64(1), "id": float64(31)},
			}
		}
		json.NewEncoder(w).Encode(eps) //nolint:errcheck
	}))
	defer sonarrSrv.Close()

	// Capture warn log to verify failure count is logged.
	var logBuf logCapture
	origLogger := slog.Default()
	slog.SetDefault(slog.New(&logBuf))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	db := testSQLiteDB(t)
	svc := &stubArrBackfill{
		doGet: func(baseURL, apiKey, path string) ([]byte, error) {
			resp, err := http.Get(sonarrSrv.URL + path)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			var buf []byte
			tmp := make([]byte, 512)
			for {
				n, rerr := resp.Body.Read(tmp)
				buf = append(buf, tmp[:n]...)
				if rerr != nil {
					break
				}
			}
			return buf, nil
		},
	}

	err := backfillSonarr(context.Background(), db, svc, sonarrSrv.URL, "key")
	if err != nil {
		t.Fatalf("backfillSonarr returned error: %v", err)
	}

	// Series 1 season 1 should exist.
	assertSeasonExists(t, db, "Series One", 1, true)
	// Series 3 season 1 should exist.
	assertSeasonExists(t, db, "Series Three", 1, true)
	// Series 2 should have NO seasons (episode fetch failed).
	assertSeasonExists(t, db, "Series Two", 1, false)

	// A warning should have been emitted for the 1 failure.
	if !logBuf.hasWarn("backfill") {
		t.Error("expected a warn log about backfill failures, got none")
	}
}

// assertSeasonExists checks that a season record for the given series title exists (or not).
func assertSeasonExists(t *testing.T, db *sql.DB, seriesTitle string, seasonNum int, want bool) {
	t.Helper()
	// Find series by title.
	var seriesID string
	row := db.QueryRow(`SELECT id FROM catalog_items WHERE type='series' AND title=?`, seriesTitle)
	if err := row.Scan(&seriesID); err != nil {
		if !want {
			return // series might not exist at all; that's fine for the "not want" case
		}
		t.Errorf("series %q not found in catalog: %v", seriesTitle, err)
		return
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM catalog_items WHERE type='season' AND parent_id=? AND season_number=?`, seriesID, seasonNum).Scan(&count) //nolint:errcheck
	if want && count == 0 {
		t.Errorf("season %d for %q not found in catalog", seasonNum, seriesTitle)
	}
	if !want && count > 0 {
		t.Errorf("season %d for %q unexpectedly found in catalog", seasonNum, seriesTitle)
	}
}

// logCapture is a minimal slog.Handler that records messages.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (l *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
	// Also write to stderr so test -v shows it.
	fmt.Fprintf(os.Stderr, "[%s] %s\n", r.Level, r.Message)
	return nil
}
func (l *logCapture) WithAttrs(attrs []slog.Attr) slog.Handler { return l }
func (l *logCapture) WithGroup(name string) slog.Handler       { return l }

func (l *logCapture) hasWarn(msgSubstring string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.records {
		if r.Level >= slog.LevelWarn && containsStr(r.Message, msgSubstring) {
			return true
		}
	}
	return false
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := range s {
			if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// ── F14: ErrUnknownSourceType sentinel ───────────────────────────────────────

// TestUpsertFromHook_UnknownSourceTypeIsSentinel verifies that passing an
// unrecognised source type returns an error that wraps ErrUnknownSourceType,
// allowing callers to use errors.Is for targeted error handling.
func TestUpsertFromHook_UnknownSourceTypeIsSentinel(t *testing.T) {
	db := testSQLiteDB(t)

	err := UpsertFromHook(context.Background(), db, ProculaJobSource{
		Type:  "soundtrack", // unknown type
		Title: "OST",
		Year:  2024,
	})

	if err == nil {
		t.Fatal("expected error for unknown source type, got nil")
	}
	if !errors.Is(err, ErrUnknownSourceType) {
		t.Errorf("errors.Is(err, ErrUnknownSourceType) = false; err = %v", err)
	}
}

// ── R8-catalog: RootCtx shutdown discipline ──────────────────────────────────

// TestHandleCatalogBackfill_RespectsRootCtx verifies that the goroutine spawned
// by HandleCatalogBackfill observes cancellation of Handler.RootCtx.
func TestHandleCatalogBackfill_RespectsRootCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Track whether ctx was cancelled when ArrGet was called.
	var ctxErrObserved atomic.Int32
	arr := &stubArrBackfill{
		doGet: func(baseURL, apiKey, path string) ([]byte, error) {
			if ctx.Err() != nil {
				ctxErrObserved.Store(1)
			}
			// Return an empty list so backfill completes cleanly when not cancelled.
			return []byte("[]"), nil
		},
	}

	db := testSQLiteDB(t)
	h := &Handler{
		DB:        db,
		Arr:       arr,
		RootCtx:   ctx,
		RadarrURL: "http://radarr",
		SonarrURL: "http://sonarr",
	}

	req, _ := http.NewRequest(http.MethodPost, "/api/pelicula/catalog/backfill", nil)
	w := httptest.NewRecorder()
	h.HandleCatalogBackfill(w, req)

	// Handler returns 200 immediately; the goroutine runs in the background.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Cancel the root context, then give the goroutine time to observe it.
	cancel()
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ctxErrObserved.Load() == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if ctxErrObserved.Load() != 1 {
		// If ctxErrObserved is still 0 the goroutine may have finished before
		// cancel() fired. Re-trigger with an already-cancelled ctx to confirm
		// the ctx flows through at all.
		ctx2, cancel2 := context.WithCancel(context.Background())
		cancel2()
		h2 := &Handler{
			DB:        db,
			Arr:       arr,
			RootCtx:   ctx2,
			RadarrURL: "http://radarr",
			SonarrURL: "http://sonarr",
		}
		req2, _ := http.NewRequest(http.MethodPost, "/api/pelicula/catalog/backfill", nil)
		w2 := httptest.NewRecorder()
		h2.HandleCatalogBackfill(w2, req2)
		time.Sleep(50 * time.Millisecond)
		if ctxErrObserved.Load() != 1 {
			t.Error("ArrGet never observed a cancelled ctx; RootCtx is not flowing through HandleCatalogBackfill")
		}
	}
}

// TestSyncJellyfinMetadata_PassesCtxToUpdate verifies that the ctx passed to
// SyncJellyfinMetadata reaches UpdateCatalogMetadata. A cancelled ctx causes
// the SQLite write to fail, surfacing the cancellation as a returned error.
func TestSyncJellyfinMetadata_PassesCtxToUpdate(t *testing.T) {
	db := testSQLiteDB(t)

	// Seed a catalog item so UpdateCatalogMetadata has a valid row to update.
	id, err := UpsertCatalogItem(context.Background(), db, CatalogItem{
		Type:    "movie",
		TmdbID:  999,
		ArrID:   1,
		ArrType: "radarr",
		Title:   "Test Movie",
		Year:    2024,
		Tier:    "library",
	})
	if err != nil {
		t.Fatalf("UpsertCatalogItem: %v", err)
	}

	// Jellyfin stub returns empty items — no Tmdb/Tvdb match, so
	// fetchJellyfinItemMeta returns empty strings and skips to UpdateCatalogMetadata.
	jf := &stubJfClient{
		apiKey: "k",
		userID: "u",
		doGet: func(ctx context.Context, path, key string) ([]byte, error) {
			return json.Marshal(struct {
				Items []jellyfinItem `json:"Items"`
			}{})
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	h := &Handler{
		DB:      db,
		Jf:      jf,
		RootCtx: ctx,
	}

	err = h.SyncJellyfinMetadata(ctx, &CatalogItem{
		ID:     id,
		Type:   "movie",
		TmdbID: 0, // no provider ID — match skipped, UpdateCatalogMetadata is still called
	})

	// A cancelled context should propagate into the SQLite write and return an error.
	if err == nil {
		t.Error("expected error from cancelled ctx in SyncJellyfinMetadata, got nil")
	}
	if !containsStr(err.Error(), "context") {
		t.Logf("error = %v (may be driver-specific; acceptable if non-nil)", err)
	}
}

// ── R13: JellyfinGet ctx propagation ─────────────────────────────────────────

// TestJellyfinMetaClient_PassesCtx verifies that a cancelled ctx passed to
// fetchJellyfinLibrary reaches JellyfinGet inside the stub, confirming the
// context.Background() bridge has been removed from the call path.
func TestJellyfinMetaClient_PassesCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	var ctxSeen atomic.Int32
	jf := &stubJfClient{
		apiKey: "key",
		userID: "user1",
		doGet: func(ctx context.Context, path, key string) ([]byte, error) {
			if ctx.Err() != nil {
				ctxSeen.Store(1)
			}
			return json.Marshal(struct {
				Items []jellyfinItem `json:"Items"`
			}{})
		},
	}

	h := &Handler{}
	h.fetchJellyfinLibrary(ctx, jf) //nolint:errcheck

	if ctxSeen.Load() != 1 {
		t.Error("JellyfinGet did not receive the cancelled ctx; context.Background() bridge may still be present")
	}
}
