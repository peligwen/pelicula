package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
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
	doGet  func(path, key string) ([]byte, error)
}

func (s *stubJfClient) GetJellyfinAPIKey() string   { return s.apiKey }
func (s *stubJfClient) GetJellyfinUserID() string   { return s.userID }
func (s *stubJfClient) SetJellyfinUserID(id string) { s.userID = id }
func (s *stubJfClient) JellyfinGet(path, apiKey string) ([]byte, error) {
	if s.doGet != nil {
		return s.doGet(path, apiKey)
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
		doGet: func(path, key string) ([]byte, error) {
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
			h.fetchJellyfinLibrary(jf) //nolint:errcheck
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
func (s *stubArrBackfill) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	if s.doGet != nil {
		return s.doGet(baseURL, apiKey, path)
	}
	return nil, fmt.Errorf("stub: unexpected ArrGet %q", path)
}
func (s *stubArrBackfill) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return nil, nil
}
func (s *stubArrBackfill) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return nil, nil
}
func (s *stubArrBackfill) ArrDelete(baseURL, apiKey, path string) ([]byte, error) {
	return nil, nil
}
func (s *stubArrBackfill) ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error) {
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
