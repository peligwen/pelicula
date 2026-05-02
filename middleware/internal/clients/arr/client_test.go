// Package arr — tests for the typed *arr HTTP client.
package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestClient builds a Client pointed at srv with a fixed fake key.
func newTestClient(srvURL string) *Client {
	return New(srvURL, "fakeapikey")
}

func TestGetMovie_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1,"title":"Foo"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.GetMovie(context.Background(), "/api/v3", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["id"].(float64) != 1 {
		t.Errorf("expected id=1, got %v", got["id"])
	}
	if got["title"] != "Foo" {
		t.Errorf("expected title=Foo, got %v", got["title"])
	}
}

func TestGetMovie_PathShape(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":42}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetMovie(context.Background(), "/api/v3", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/movie/42" {
		t.Errorf("expected path /api/v3/movie/42, got %q", gotPath)
	}
}

func TestGetSeries_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"title":"Breaking Bad"},{"id":2,"title":"Fargo"}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.GetSeries(context.Background(), "/api/v3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 series, got %d", len(got))
	}
	if got[0]["title"] != "Breaking Bad" {
		t.Errorf("expected first title Breaking Bad, got %v", got[0]["title"])
	}
}

func TestTriggerCommand_PostsToCommand(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	payload := map[string]any{"name": "RescanMovie", "movieId": 7}
	err := c.TriggerCommand(context.Background(), "/api/v3", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/command" {
		t.Errorf("expected path /api/v3/command, got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
	if gotBody["name"] != "RescanMovie" {
		t.Errorf("expected name=RescanMovie in body, got %v", gotBody["name"])
	}
}

func TestSearch_QueryEscape(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	term := "Tom & Jerry"
	_, err := c.Search(context.Background(), term)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotQuery != term {
		t.Errorf("expected query round-trip to %q, got %q", term, gotQuery)
	}
	// also check that the raw query contains the URL-escaped form
}

func TestGetMoviesByPath_QueryEscape(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	mediaPath := "/media/My Movies/foo bar"
	_, err := c.GetMoviesByPath(context.Background(), "/api/v3", mediaPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// url.QueryEscape encodes spaces as +
	wantEncoded := url.QueryEscape(mediaPath)
	if !strings.Contains(gotRawQuery, wantEncoded) {
		t.Errorf("expected raw query to contain %q, got %q", wantEncoded, gotRawQuery)
	}
}

func TestApiKeyHeaderSet(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetMovie(context.Background(), "/api/v3", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKey != "fakeapikey" {
		t.Errorf("expected X-Api-Key=fakeapikey, got %q", gotKey)
	}
}

func TestRetryOn5xx_Inherited(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "fakeapikey")
	c.base.Retry.Delay = 1 * time.Millisecond

	_, err := c.GetMovie(context.Background(), "/api/v3", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts)
	}
}

func TestErrorRedaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "fakeapikey")
	c.base.Retry.Delay = 1 * time.Millisecond
	c.base.Retry.MaxAttempts = 1

	// The path contains a sensitive query param; httpx should redact it.
	_, err := c.Get(context.Background(), "/api/v3/movie?apikey=secretvalue123")
	if err == nil {
		t.Fatal("expected an error from 500, got nil")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "secretvalue123") {
		t.Errorf("error string must not contain the raw secret: %q", errStr)
	}
	if !strings.Contains(errStr, "REDACTED") {
		t.Errorf("expected REDACTED in error string, got %q", errStr)
	}
}

// ── Lookup tests ─────────────────────────────────────────────────────────────

func TestLookupMovie_PathAndParse(t *testing.T) {
	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"title":"Foo Bar","tmdbId":123}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.LookupMovie(context.Background(), "/api/v3", "Foo Bar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/movie/lookup" {
		t.Errorf("expected path /api/v3/movie/lookup, got %q", gotPath)
	}
	if !strings.Contains(gotRawQuery, "term=") {
		t.Errorf("expected term= in query, got %q", gotRawQuery)
	}
	// url.QueryEscape encodes spaces as +
	if !strings.Contains(gotRawQuery, url.QueryEscape("Foo Bar")) {
		t.Errorf("expected encoded term in query, got %q", gotRawQuery)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0]["title"] != "Foo Bar" {
		t.Errorf("expected title=Foo Bar, got %v", got[0]["title"])
	}
}

func TestLookupMovieByTmdbID_PathAndParse(t *testing.T) {
	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"title":"Inception","tmdbId":27205}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.LookupMovieByTmdbID(context.Background(), "/api/v3", 27205)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/movie/lookup/tmdb" {
		t.Errorf("expected path /api/v3/movie/lookup/tmdb, got %q", gotPath)
	}
	if gotRawQuery != "tmdbId=27205" {
		t.Errorf("expected query tmdbId=27205, got %q", gotRawQuery)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0]["title"] != "Inception" {
		t.Errorf("expected title=Inception, got %v", got[0]["title"])
	}
}

func TestLookupSeries_PathAndParse(t *testing.T) {
	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"title":"The Wire","tvdbId":79126}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.LookupSeries(context.Background(), "/api/v3", "The Wire")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/series/lookup" {
		t.Errorf("expected path /api/v3/series/lookup, got %q", gotPath)
	}
	if !strings.Contains(gotRawQuery, "term=") {
		t.Errorf("expected term= in query, got %q", gotRawQuery)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0]["title"] != "The Wire" {
		t.Errorf("expected title=The Wire, got %v", got[0]["title"])
	}
}

// ── UpdateNotification / UpdateDownloadClient tests ───────────────────────────

func TestUpdateNotification_PathAndMethod(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	payload := map[string]any{"name": "webhook", "onDownload": true}
	err := c.UpdateNotification(context.Background(), "/api/v3", 7, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/notification/7" {
		t.Errorf("expected path /api/v3/notification/7, got %q", gotPath)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %q", gotMethod)
	}
	if gotBody["name"] != "webhook" {
		t.Errorf("expected name=webhook in body, got %v", gotBody["name"])
	}
}

func TestUpdateDownloadClient_PathAndMethod(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.UpdateDownloadClient(context.Background(), "/api/v3", 3, map[string]any{"name": "qbt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/downloadclient/3" {
		t.Errorf("expected path /api/v3/downloadclient/3, got %q", gotPath)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %q", gotMethod)
	}
}

// ── GetAllQueueRecords pagination test ────────────────────────────────────────

func TestGetAllQueueRecords_Pagination(t *testing.T) {
	var mu sync.Mutex
	var pagesHit []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageStr := r.URL.Query().Get("page")
		mu.Lock()
		pagesHit = append(pagesHit, pageStr)
		mu.Unlock()

		// Build 100 records for pages 1 and 2, 50 records for page 3.
		var records []map[string]any
		switch pageStr {
		case "1", "2":
			for i := 0; i < 100; i++ {
				records = append(records, map[string]any{"id": i})
			}
		case "3":
			for i := 0; i < 50; i++ {
				records = append(records, map[string]any{"id": i})
			}
		}

		resp := map[string]any{
			"totalRecords": 250,
			"records":      records,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	all, err := c.GetAllQueueRecords(context.Background(), "/api/v3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 250 {
		t.Errorf("expected 250 total records, got %d", len(all))
	}

	mu.Lock()
	hitCount := len(pagesHit)
	mu.Unlock()

	if hitCount != 3 {
		t.Errorf("expected exactly 3 page requests, got %d (pages: %v)", hitCount, pagesHit)
	}
	// Verify the distinct page numbers hit were 1, 2, and 3.
	pageSet := make(map[string]bool)
	for _, p := range pagesHit {
		pageSet[p] = true
	}
	for _, want := range []string{"1", "2", "3"} {
		if !pageSet[want] {
			t.Errorf("expected page %s to be requested, got pages: %v", want, pagesHit)
		}
	}
}

func TestGetAllQueueRecords_ExtraParams(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		resp := map[string]any{"totalRecords": 0, "records": []any{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetAllQueueRecords(context.Background(), "/api/v3", "&includeUnknownMovieItems=true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotRawQuery, "includeUnknownMovieItems=true") {
		t.Errorf("expected includeUnknownMovieItems=true in query, got %q", gotRawQuery)
	}
}

// ── Release profiles tests ────────────────────────────────────────────────────

func TestListReleaseProfiles_PathAndParse(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"No HDTV"},{"id":2,"name":"720p+"}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.ListReleaseProfiles(context.Background(), "/api/v3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/releaseprofile" {
		t.Errorf("expected path /api/v3/releaseprofile, got %q", gotPath)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(got))
	}
	if got[0]["name"] != "No HDTV" {
		t.Errorf("expected name=No HDTV, got %v", got[0]["name"])
	}
}

func TestAddReleaseProfile_PathAndMethod(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.AddReleaseProfile(context.Background(), "/api/v3", map[string]any{"name": "Preferred"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/releaseprofile" {
		t.Errorf("expected path /api/v3/releaseprofile, got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
}

func TestUpdateReleaseProfile_PathAndMethod(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.UpdateReleaseProfile(context.Background(), "/api/v3", 5, map[string]any{"name": "Updated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/releaseprofile/5" {
		t.Errorf("expected path /api/v3/releaseprofile/5, got %q", gotPath)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %q", gotMethod)
	}
}

// ── GetHistory / GetMissing tests ─────────────────────────────────────────────

func TestGetHistory_PathAndRawBytes(t *testing.T) {
	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"records":[{"id":1,"sourceTitle":"foo.mkv"}]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	raw, err := c.GetHistory(context.Background(), "/api/v3", "?pageSize=20&sortKey=date")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/history" {
		t.Errorf("expected path /api/v3/history, got %q", gotPath)
	}
	if !strings.Contains(gotRawQuery, "pageSize=20") {
		t.Errorf("expected pageSize=20 in query, got %q", gotRawQuery)
	}
	if !strings.Contains(string(raw), "sourceTitle") {
		t.Errorf("expected raw bytes to contain sourceTitle, got %s", raw)
	}
}

func TestGetHistory_NoExtraParams(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetHistory(context.Background(), "/api/v3", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/history" {
		t.Errorf("expected path /api/v3/history, got %q", gotPath)
	}
}

func TestGetMissing_PathAndRawBytes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"records":[{"id":99,"title":"Missing Movie"}]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	raw, err := c.GetMissing(context.Background(), "/api/v3", "?pageSize=50")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/wanted/missing" {
		t.Errorf("expected path /api/v3/wanted/missing, got %q", gotPath)
	}
	if !strings.Contains(string(raw), "Missing Movie") {
		t.Errorf("expected raw bytes to contain Missing Movie, got %s", raw)
	}
}

// ── ListIndexers test ─────────────────────────────────────────────────────────

func TestListIndexers_PathAndParse(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"NZBGeek"},{"id":2,"name":"NZBFinder"}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.ListIndexers(context.Background(), "/api/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/indexer" {
		t.Errorf("expected path /api/v1/indexer, got %q", gotPath)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 indexers, got %d", len(got))
	}
	if got[0]["name"] != "NZBGeek" {
		t.Errorf("expected name=NZBGeek, got %v", got[0]["name"])
	}
}
